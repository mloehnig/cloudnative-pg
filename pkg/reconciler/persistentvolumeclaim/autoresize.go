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
	"strings"
	"time"

	"github.com/cloudnative-pg/machinery/pkg/log"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
)

// volumeNamePgData is the VolumeUsage.Name reported by the instance manager for
// the primary data volume.
const volumeNamePgData = "pgdata"

const (
	autoResizeWindow     = 24 * time.Hour
	autoResizeHistoryCap = 10
)

var storageAutoResizeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "cnpg",
	Subsystem: "cluster",
	Name:      "storage_autoresize_total",
	Help:      "Number of automatic PVC resize evaluations by volume and result",
}, []string{"volume", "result"})

func init() {
	ctrlmetrics.Registry.MustRegister(storageAutoResizeTotal)
}

// volumeNameForPVCRole maps a PVC's role label to the VolumeUsage.Name the
// instance manager reports (see Part 1: "pgdata" | "wal" | "tbs-<name>").
func volumeNameForPVCRole(labels map[string]string) (string, bool) {
	switch utils.PVCRole(labels[utils.PvcRoleLabelName]) {
	case utils.PVCRolePgData:
		return volumeNamePgData, true
	case utils.PVCRolePgWal:
		return "wal", true
	case utils.PVCRolePgTablespace:
		tbs := labels[utils.TablespaceNameLabelName]
		if tbs == "" {
			return "", false
		}
		return "tbs-" + tbs, true
	default:
		return "", false
	}
}

// volumeUsageForPVC finds the disk usage the owning instance reported for the
// volume backing this PVC. Returns false when no matching usage is available.
func volumeUsageForPVC(
	pvc *corev1.PersistentVolumeClaim,
	statuses postgres.PostgresqlStatusList,
) (usageBytes, bool) {
	instanceName := pvc.Labels[utils.InstanceNameLabelName]
	volumeName, ok := volumeNameForPVCRole(pvc.Labels)
	if instanceName == "" || !ok {
		return usageBytes{}, false
	}

	for i := range statuses.Items {
		item := &statuses.Items[i]
		if item.Pod == nil || item.Pod.Name != instanceName {
			continue
		}
		for _, vu := range item.VolumeUsages {
			if vu.Name == volumeName {
				return usageBytes{Total: vu.TotalBytes, Used: vu.UsedBytes, Available: vu.AvailableBytes}, true
			}
		}
	}
	return usageBytes{}, false
}

// ReconcileAutoResize grows PVCs whose reported usage crosses their configured
// auto-resize triggers. It patches PVC storage requests only; it never mutates
// the Cluster spec. History and the auto-resize condition are persisted to the
// Cluster status.
func ReconcileAutoResize(
	ctx context.Context,
	c client.Client,
	recorder record.EventRecorder,
	cluster *apiv1.Cluster,
	pvcs []corev1.PersistentVolumeClaim,
	statuses postgres.PostgresqlStatusList,
) error {
	return reconcileAutoResize(ctx, c, recorder, cluster, pvcs, statuses, time.Now())
}

func reconcileAutoResize(
	ctx context.Context,
	c client.Client,
	recorder record.EventRecorder,
	cluster *apiv1.Cluster,
	pvcs []corev1.PersistentVolumeClaim,
	statuses postgres.PostgresqlStatusList,
	now time.Time,
) error {
	contextLogger := log.FromContext(ctx)
	origCluster := cluster.DeepCopy()
	statusChanged := false
	blockedReason := apiv1.ConditionReason("")
	blockedMessage := ""
	performed := false

	for idx := range pvcs {
		pvc := &pvcs[idx]

		pvcRole, err := GetExpectedObjectCalculator(pvc.GetLabels())
		if err != nil {
			continue // not a role we manage; skip silently
		}
		storageConfiguration, err := pvcRole.GetStorageConfiguration(cluster)
		if err != nil || storageConfiguration.AutoResize == nil {
			continue
		}

		volumeName, _ := volumeNameForPVCRole(pvc.GetLabels())
		usage, ok := volumeUsageForPVC(pvc, statuses)
		if !ok {
			continue // no usage reported yet
		}

		current := pvc.Spec.Resources.Requests["storage"]
		recent := countRecentResizes(cluster.Status.StorageResizeHistory[pvc.Name], now, autoResizeWindow)
		outcome := evaluateAutoResize(*storageConfiguration.AutoResize, current, usage, recent)

		if !outcome.Resize {
			// Surface actionable skip states (at-limit / budget) as warnings.
			switch {
			case strings.Contains(outcome.SkipReason, "limit"):
				storageAutoResizeTotal.WithLabelValues(volumeName, "at_limit").Inc()
				recorder.Event(cluster, "Warning", "StorageAutoResizeAtLimit",
					pvc.Name+": "+outcome.SkipReason)
				blockedReason = apiv1.StorageAutoResizeAtLimit
				blockedMessage = pvc.Name + " is at its configured storage limit"
			case strings.Contains(outcome.SkipReason, "budget"):
				storageAutoResizeTotal.WithLabelValues(volumeName, "budget_exhausted").Inc()
				recorder.Event(cluster, "Warning", "StorageAutoResizeBudgetExhausted",
					pvc.Name+": "+outcome.SkipReason)
				blockedReason = apiv1.StorageAutoResizeBudgetExhausted
				blockedMessage = pvc.Name + ": " + outcome.SkipReason
			}
			continue
		}

		if err := patchPVCStorageRequest(ctx, c, pvc, outcome.NewSize); err != nil {
			storageAutoResizeTotal.WithLabelValues(volumeName, "error").Inc()
			return err
		}
		storageAutoResizeTotal.WithLabelValues(volumeName, "resized").Inc()
		performed = true
		contextLogger.Info("auto-resized PVC",
			"pvcName", pvc.Name, "from", current.String(), "to", outcome.NewSize.String(),
			"reason", outcome.Reason)
		recorder.Event(cluster, "Normal", "StorageAutoResize",
			pvc.Name+" grown from "+current.String()+" to "+outcome.NewSize.String()+": "+outcome.Reason)

		if cluster.Status.StorageResizeHistory == nil {
			cluster.Status.StorageResizeHistory = map[string][]apiv1.StorageResizeEvent{}
		}
		history := cluster.Status.StorageResizeHistory[pvc.Name]
		history = append(history, apiv1.StorageResizeEvent{
			Timestamp: metav1.NewTime(now),
			FromSize:  current.String(),
			ToSize:    outcome.NewSize.String(),
			Reason:    outcome.Reason,
		})
		pruned := pruneResizeHistory(history, now, autoResizeWindow, autoResizeHistoryCap)
		cluster.Status.StorageResizeHistory[pvc.Name] = pruned
		statusChanged = true
	}

	// Reflect the outcome as a cluster condition.
	switch {
	case blockedReason != "":
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    string(apiv1.ConditionStorageAutoResize),
			Status:  metav1.ConditionFalse,
			Reason:  string(blockedReason),
			Message: blockedMessage,
		})
		statusChanged = true
	case performed:
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:    string(apiv1.ConditionStorageAutoResize),
			Status:  metav1.ConditionTrue,
			Reason:  string(apiv1.StorageAutoResizePerformed),
			Message: "storage auto-resize performed",
		})
	}

	if statusChanged {
		if err := c.Status().Patch(ctx, cluster, client.MergeFrom(origCluster)); err != nil {
			return err
		}
	}
	return nil
}
