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

	"github.com/cloudnative-pg/cloudnative-pg/pkg/postgres"
	"github.com/cloudnative-pg/cloudnative-pg/pkg/utils"
)

// volumeNamePgData is the VolumeUsage.Name reported by the instance manager for
// the primary data volume.
const volumeNamePgData = "pgdata"

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
