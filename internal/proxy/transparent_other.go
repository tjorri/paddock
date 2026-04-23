//go:build !linux

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

package proxy

import (
	"errors"
	"net"
)

// originalDestination is a no-op outside Linux. The proxy's Darwin
// builds exist only for developer ergonomics; the in-cluster binary is
// always Linux, where SO_ORIGINAL_DST is supported.
func originalDestination(_ net.Conn) (net.IP, int, error) {
	return nil, 0, errors.New("SO_ORIGINAL_DST is only available on Linux")
}

// TransparentInterceptionSupported reports whether the current build
// has a working SO_ORIGINAL_DST implementation.
func TransparentInterceptionSupported() bool { return false }
