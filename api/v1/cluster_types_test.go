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

package v1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("StorageResizeHistory deepcopy", func() {
	It("deep-copies the history map", func() {
		status := &ClusterStatus{
			StorageResizeHistory: map[string][]StorageResizeEvent{
				"cluster-1": {{FromSize: "10Gi", ToSize: "12Gi", Reason: "threshold"}},
			},
		}
		clone := status.DeepCopy()
		clone.StorageResizeHistory["cluster-1"][0].ToSize = "20Gi"
		Expect(status.StorageResizeHistory["cluster-1"][0].ToSize).To(Equal("12Gi"))
	})
})

var _ = Describe("StorageAutoResize deepcopy", func() {
	It("deep-copies pointer fields independently", func() {
		orig := &StorageConfiguration{
			Size: "10Gi",
			AutoResize: &StorageAutoResize{
				UsageThreshold: 80,
				Step:           "20%",
				MinStep:        ptr.To(resource.MustParse("2Gi")),
				Limit:          ptr.To(resource.MustParse("100Gi")),
			},
		}
		clone := orig.DeepCopy()
		Expect(clone.AutoResize).NotTo(BeIdenticalTo(orig.AutoResize))
		clone.AutoResize.UsageThreshold = 50
		clone.AutoResize.MinStep = ptr.To(resource.MustParse("5Gi"))
		Expect(orig.AutoResize.UsageThreshold).To(BeNumerically("==", 80))
		Expect(orig.AutoResize.MinStep.String()).To(Equal("2Gi"))
	})
})
