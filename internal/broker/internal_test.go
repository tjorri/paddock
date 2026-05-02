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

package broker

import (
	"context"

	brokerapi "github.com/tjorri/paddock/internal/broker/api"
)

// SubstituteBurstForTest exposes the unexported substituteBurst constant
// so external tests can drain the bucket without hard-coding the value.
const SubstituteBurstForTest = substituteBurst

// SubstituteAuthForTest exposes the unexported substituteAuth method
// to the external broker_test package. Test-only — not for use by
// non-test code.
func (s *Server) SubstituteAuthForTest(
	ctx context.Context,
	runNamespace, runName string,
	req brokerapi.SubstituteAuthRequest,
) (brokerapi.SubstituteAuthResponse, *CredentialAudit, error) {
	return s.substituteAuth(ctx, runNamespace, runName, req)
}

// ApplicationErrorForTest is a type alias exposing the unexported
// applicationError type so external tests can errors.As against it.
type ApplicationErrorForTest = applicationError

// Status exposes applicationError.status to test code.
func (e *applicationError) Status() int { return e.status }

// Code exposes applicationError.code to test code.
func (e *applicationError) Code() string { return e.code }
