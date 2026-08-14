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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
