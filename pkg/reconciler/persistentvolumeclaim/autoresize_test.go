/*
Copyright © contributors to CloudNativePG, established as
CloudNativePG a Series of LF Projects, LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

SPDX-License-Identifier: Apache-2.0
*/

package persistentvolumeclaim

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("volumeUsageForPVC", func() {
	statuses := postgres.PostgresqlStatusList{
		Items: []postgres.PostgresqlStatus{
			{
				Pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"}},
				VolumeUsages: []postgres.VolumeUsage{
					{Name: "pgdata", TotalBytes: 100, UsedBytes: 40, AvailableBytes: 55},
					{Name: "wal", TotalBytes: 200, UsedBytes: 20, AvailableBytes: 175},
				},
			},
		},
	}

	pvc := func(instance, role, tbs string) *corev1.PersistentVolumeClaim {
		labels := map[string]string{
			utils.InstanceNameLabelName: instance,
			utils.PvcRoleLabelName:      role,
		}
		if tbs != "" {
			labels[utils.TablespaceNameLabelName] = tbs
		}
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Labels: labels}}
	}

	It("finds the pgdata usage for the matching instance", func() {
		u, ok := volumeUsageForPVC(pvc("cluster-1", string(utils.PVCRolePgData), ""), statuses)
		Expect(ok).To(BeTrue())
		Expect(u.Total).To(BeNumerically("==", 100))
		Expect(u.Used).To(BeNumerically("==", 40))
	})

	It("finds the wal usage", func() {
		u, ok := volumeUsageForPVC(pvc("cluster-1", string(utils.PVCRolePgWal), ""), statuses)
		Expect(ok).To(BeTrue())
		Expect(u.Total).To(BeNumerically("==", 200))
	})

	It("returns false when the instance has no reported usage", func() {
		_, ok := volumeUsageForPVC(pvc("cluster-2", string(utils.PVCRolePgData), ""), statuses)
		Expect(ok).To(BeFalse())
	})

	It("returns false when the named volume is absent", func() {
		_, ok := volumeUsageForPVC(pvc("cluster-1", string(utils.PVCRolePgTablespace), "atlas"), statuses)
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("ReconcileAutoResize", func() {
	const ns = "default"

	makePVC := func(name, instance, role, size string) corev1.PersistentVolumeClaim {
		return corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					utils.InstanceNameLabelName: instance,
					utils.PvcRoleLabelName:      role,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{"storage": resource.MustParse(size)},
				},
			},
		}
	}

	It("grows a PVC over threshold and records history", func() {
		pvc := makePVC("cluster-1", "cluster-1", string(utils.PVCRolePgData), "10Gi")
		cluster := &apiv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: ns},
			Spec: apiv1.ClusterSpec{
				StorageConfiguration: apiv1.StorageConfiguration{
					Size: "10Gi",
					AutoResize: &apiv1.StorageAutoResize{
						UsageThreshold: 80, Step: "20%",
						MinStep:          ptr.To(resource.MustParse("2Gi")),
						MaxStep:          ptr.To(resource.MustParse("500Gi")),
						MaxResizesPerDay: 3, AcknowledgeWALRisk: true,
					},
				},
			},
		}
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(apiv1.AddToScheme(scheme)).To(Succeed())
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(&pvc, cluster).WithStatusSubresource(cluster).Build()

		statuses := postgres.PostgresqlStatusList{Items: []postgres.PostgresqlStatus{{
			Pod:          &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"}},
			VolumeUsages: []postgres.VolumeUsage{{Name: "pgdata", TotalBytes: 100, UsedBytes: 90, AvailableBytes: 10}},
		}}}
		rec := record.NewFakeRecorder(10)

		Expect(ReconcileAutoResize(context.Background(), cli, rec, cluster,
			[]corev1.PersistentVolumeClaim{pvc}, statuses)).To(Succeed())

		var got corev1.PersistentVolumeClaim
		Expect(cli.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "cluster-1"}, &got)).To(Succeed())
		size := got.Spec.Resources.Requests["storage"]
		expected12Gi := resource.MustParse("12Gi")
		Expect(size.Value()).To(Equal(expected12Gi.Value()))
		var gotCluster apiv1.Cluster
		Expect(cli.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "cluster-1"}, &gotCluster)).To(Succeed())
		Expect(gotCluster.Status.StorageResizeHistory["cluster-1"]).To(HaveLen(1))
	})

	It("does not resize when usage is below threshold", func() {
		pvc := makePVC("cluster-1", "cluster-1", string(utils.PVCRolePgData), "10Gi")
		cluster := &apiv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-1", Namespace: ns},
			Spec: apiv1.ClusterSpec{StorageConfiguration: apiv1.StorageConfiguration{
				Size: "10Gi",
				AutoResize: &apiv1.StorageAutoResize{
					UsageThreshold: 80, Step: "20%", MaxResizesPerDay: 3, AcknowledgeWALRisk: true,
				},
			}},
		}
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(apiv1.AddToScheme(scheme)).To(Succeed())
		cli := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(&pvc, cluster).WithStatusSubresource(cluster).Build()
		statuses := postgres.PostgresqlStatusList{Items: []postgres.PostgresqlStatus{{
			Pod:          &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"}},
			VolumeUsages: []postgres.VolumeUsage{{Name: "pgdata", TotalBytes: 100, UsedBytes: 10, AvailableBytes: 85}},
		}}}

		Expect(ReconcileAutoResize(context.Background(), cli, record.NewFakeRecorder(10),
			cluster, []corev1.PersistentVolumeClaim{pvc}, statuses)).To(Succeed())

		var got corev1.PersistentVolumeClaim
		Expect(cli.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "cluster-1"}, &got)).To(Succeed())
		size := got.Spec.Resources.Requests["storage"]
		expected10Gi := resource.MustParse("10Gi")
		Expect(size.Value()).To(Equal(expected10Gi.Value()))
		var gotCluster apiv1.Cluster
		Expect(cli.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "cluster-1"}, &gotCluster)).To(Succeed())
		Expect(gotCluster.Status.StorageResizeHistory).To(BeEmpty())
	})
})
