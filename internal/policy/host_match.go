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

package policy

import "strings"

// AnyHostMatches reports whether any of the grant patterns matches the
// required host under EgressHostMatches semantics. Whitespace is
// trimmed on both sides for defence against operator-typed list entries
// (admission rejects whitespace, but providers historically trimmed
// here too — preserving that behaviour avoids a silent semantic shift
// during the B-03 host-match consolidation).
func AnyHostMatches(grants []string, required string) bool {
	r := strings.TrimSpace(required)
	for _, g := range grants {
		if EgressHostMatches(strings.TrimSpace(g), r) {
			return true
		}
	}
	return false
}
