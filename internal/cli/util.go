/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"io"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readAll slurps a reader into a string.
func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// findCondition returns a pointer to the condition of the given type,
// or nil when absent. Matches the helper used by the controller tests.
func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

// age returns a short human-readable duration since t (e.g. "14s", "3m",
// "2h", "4d").
func age(t metav1.Time) string {
	if t.IsZero() {
		return "<none>"
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return shortFmt(int(d.Seconds()), "s")
	case d < time.Hour:
		return shortFmt(int(d.Minutes()), "m")
	case d < 24*time.Hour:
		return shortFmt(int(d.Hours()), "h")
	default:
		return shortFmt(int(d.Hours()/24), "d")
	}
}

func shortFmt(n int, unit string) string {
	if n <= 0 {
		return "0" + unit
	}
	return strconv.Itoa(n) + unit
}
