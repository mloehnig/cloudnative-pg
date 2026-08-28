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
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("evaluateAutoResize", func() {
	base := apiv1.StorageAutoResize{
		UsageThreshold:   80,
		Step:             "20%",
		MinStep:          ptr.To(resource.MustParse("2Gi")),
		MaxStep:          ptr.To(resource.MustParse("500Gi")),
		MaxResizesPerDay: 3,
	}
	tenGi := resource.MustParse("10Gi")

	It("does not resize below the usage threshold", func() {
		u := usageBytes{Total: 100, Used: 50, Available: 50}
		out := evaluateAutoResize(base, tenGi, u, 0)
		Expect(out.Resize).To(BeFalse())
		Expect(out.SkipReason).NotTo(BeEmpty())
	})

	It("resizes when usage crosses the threshold, clamped to minStep", func() {
		// 20% of 10Gi = 2Gi == minStep; new size 12Gi.
		u := usageBytes{Total: 100, Used: 85, Available: 15}
		out := evaluateAutoResize(base, tenGi, u, 0)
		Expect(out.Resize).To(BeTrue())
		expected := resource.MustParse("12Gi")
		Expect(out.NewSize.Value()).To(Equal(expected.Value()))
	})

	It("triggers on minAvailable even below the usage threshold", func() {
		cfg := base
		cfg.MinAvailable = ptr.To(resource.MustParse("20"))
		u := usageBytes{Total: 100, Used: 50, Available: 10} // 50% used but only 10 bytes free
		out := evaluateAutoResize(cfg, tenGi, u, 0)
		Expect(out.Resize).To(BeTrue())
	})

	It("respects the daily budget", func() {
		u := usageBytes{Total: 100, Used: 90, Available: 10}
		out := evaluateAutoResize(base, tenGi, u, 3) // already 3 today
		Expect(out.Resize).To(BeFalse())
		Expect(out.SkipReason).To(ContainSubstring("budget"))
	})

	It("treats maxResizesPerDay=-1 as unlimited", func() {
		cfg := base
		cfg.MaxResizesPerDay = -1
		u := usageBytes{Total: 100, Used: 90, Available: 10}
		out := evaluateAutoResize(cfg, tenGi, u, 100)
		Expect(out.Resize).To(BeTrue())
	})

	It("caps growth at the limit and skips when already at limit", func() {
		cfg := base
		cfg.Limit = ptr.To(resource.MustParse("11Gi"))
		u := usageBytes{Total: 100, Used: 90, Available: 10}
		out := evaluateAutoResize(cfg, tenGi, u, 0)
		Expect(out.Resize).To(BeTrue())
		expected11Gi := resource.MustParse("11Gi")
		Expect(out.NewSize.Value()).To(Equal(expected11Gi.Value()))

		out2 := evaluateAutoResize(cfg, resource.MustParse("11Gi"), u, 0)
		Expect(out2.Resize).To(BeFalse())
		Expect(out2.SkipReason).To(ContainSubstring("limit"))
	})

	It("uses an absolute step without clamping", func() {
		cfg := base
		cfg.Step = "5Gi"
		u := usageBytes{Total: 100, Used: 90, Available: 10}
		out := evaluateAutoResize(cfg, tenGi, u, 0)
		expected15Gi := resource.MustParse("15Gi")
		Expect(out.NewSize.Value()).To(Equal(expected15Gi.Value()))
	})
})

var _ = Describe("resize history helpers", func() {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	mk := func(hoursAgo int) apiv1.StorageResizeEvent {
		return apiv1.StorageResizeEvent{Timestamp: metav1.NewTime(now.Add(-time.Duration(hoursAgo) * time.Hour))}
	}

	It("counts only resizes within the window", func() {
		events := []apiv1.StorageResizeEvent{mk(1), mk(5), mk(30)}
		Expect(countRecentResizes(events, now, 24*time.Hour)).To(Equal(2))
	})

	It("prunes to the window and the max count", func() {
		events := []apiv1.StorageResizeEvent{mk(1), mk(2), mk(3), mk(48)}
		pruned := pruneResizeHistory(events, now, 24*time.Hour, 2)
		Expect(pruned).To(HaveLen(2))
		for _, e := range pruned {
			Expect(now.Sub(e.Timestamp.Time)).To(BeNumerically("<", 24*time.Hour))
		}
	})
})
