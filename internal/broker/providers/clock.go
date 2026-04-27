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

package providers

import "time"

// clockSource is the per-provider wall-clock injection point. Embedded
// in every stateful provider struct so tests can pin time without each
// provider redeclaring the same Now field + now() method (B-07).
type clockSource struct {
	// Now is the wall-clock source for TTL accounting. Zero defaults to
	// time.Now — tests inject a fixed clock.
	Now func() time.Time
}

// now returns the configured clock value, falling back to time.Now()
// when Now is unset. Cheap to call; keeps the nil-check out of every
// caller.
func (c clockSource) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
