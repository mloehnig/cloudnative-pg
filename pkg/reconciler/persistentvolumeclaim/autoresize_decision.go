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
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
)

// usageBytes is a volume's disk usage in bytes.
type usageBytes struct {
	Total     uint64
	Used      uint64
	Available uint64
}

// resizeOutcome is the result of a single auto-resize evaluation.
type resizeOutcome struct {
	Resize     bool
	NewSize    resource.Quantity
	Reason     string // why a resize was triggered (for events/history)
	SkipReason string // why no resize happened ("" when Resize is true)
}

// evaluateAutoResize decides whether and how much to grow a volume. It is pure:
// callers supply the current size, observed usage, and the count of resizes
// already performed in the rolling window.
func evaluateAutoResize(
	cfg apiv1.StorageAutoResize,
	current resource.Quantity,
	u usageBytes,
	recentResizes int,
) resizeOutcome {
	triggered, reason := autoResizeTriggered(cfg, u)
	if !triggered {
		return resizeOutcome{SkipReason: "usage below configured triggers"}
	}

	if cfg.MaxResizesPerDay >= 0 && recentResizes >= int(cfg.MaxResizesPerDay) {
		return resizeOutcome{SkipReason: fmt.Sprintf(
			"daily resize budget exhausted (%d in the last 24h)", recentResizes)}
	}

	currentBytes := current.Value()
	rawStep, isPercent, err := parseStepBytes(cfg.Step, currentBytes)
	if err != nil {
		return resizeOutcome{SkipReason: err.Error()}
	}
	step := clampStep(rawStep, isPercent, cfg)

	newBytes := currentBytes + step
	if cfg.Limit != nil && newBytes > cfg.Limit.Value() {
		newBytes = cfg.Limit.Value()
	}
	if newBytes <= currentBytes {
		return resizeOutcome{SkipReason: "already at the configured limit"}
	}

	return resizeOutcome{
		Resize:  true,
		NewSize: *resource.NewQuantity(newBytes, resource.BinarySI),
		Reason:  reason,
	}
}

func autoResizeTriggered(cfg apiv1.StorageAutoResize, u usageBytes) (bool, string) {
	if u.Total > 0 {
		usedPct := 100 * float64(u.Used) / float64(u.Total)
		if usedPct >= float64(cfg.UsageThreshold) {
			return true, fmt.Sprintf("usage %.0f%% reached threshold %d%%", usedPct, cfg.UsageThreshold)
		}
	}
	if cfg.MinAvailable != nil {
		avail := int64(u.Available) //nolint:gosec // available bytes fit int64 for any real volume
		if avail <= cfg.MinAvailable.Value() {
			return true, fmt.Sprintf("available space %d bytes at or below minAvailable %s",
				u.Available, cfg.MinAvailable.String())
		}
	}
	return false, ""
}

// parseStepBytes returns the raw step in bytes and whether it was a percentage.
func parseStepBytes(step string, currentBytes int64) (int64, bool, error) {
	if pct, ok := strings.CutSuffix(step, "%"); ok {
		n, err := strconv.Atoi(pct)
		if err != nil || n <= 0 {
			return 0, true, fmt.Errorf("invalid percentage step %q", step)
		}
		return int64(float64(currentBytes) * float64(n) / 100), true, nil
	}
	q, err := resource.ParseQuantity(step)
	if err != nil {
		return 0, false, fmt.Errorf("invalid step %q", step)
	}
	return q.Value(), false, nil
}

// clampStep bounds a percentage step to [MinStep, MaxStep]. Absolute steps are
// returned unchanged.
func clampStep(stepBytes int64, isPercent bool, cfg apiv1.StorageAutoResize) int64 {
	if !isPercent {
		return stepBytes
	}
	if cfg.MinStep != nil && stepBytes < cfg.MinStep.Value() {
		stepBytes = cfg.MinStep.Value()
	}
	if cfg.MaxStep != nil && stepBytes > cfg.MaxStep.Value() {
		stepBytes = cfg.MaxStep.Value()
	}
	return stepBytes
}

// countRecentResizes counts history entries within [now-window, now].
func countRecentResizes(events []apiv1.StorageResizeEvent, now time.Time, window time.Duration) int {
	count := 0
	for _, e := range events {
		if now.Sub(e.Timestamp.Time) < window {
			count++
		}
	}
	return count
}

// pruneResizeHistory drops entries older than the window, then keeps at most
// maxEntries of the most recent ones.
func pruneResizeHistory(
	events []apiv1.StorageResizeEvent,
	now time.Time,
	window time.Duration,
	maxEntries int,
) []apiv1.StorageResizeEvent {
	kept := make([]apiv1.StorageResizeEvent, 0, len(events))
	for _, e := range events {
		if now.Sub(e.Timestamp.Time) < window {
			kept = append(kept, e)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		return kept[i].Timestamp.After(kept[j].Timestamp.Time)
	})
	if len(kept) > maxEntries {
		kept = kept[:maxEntries]
	}
	return kept
}
