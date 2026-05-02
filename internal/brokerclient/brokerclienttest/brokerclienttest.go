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

// Package brokerclienttest is a test-only support package: it lets
// out-of-package tests in internal/controller and internal/proxy
// construct a brokerclient.Client without going through the F-29
// canonical-endpoint validator. Production code MUST NOT import this
// package.
//
// The package name is intentionally clumsy so that a grep for
// "brokerclienttest" enumerates every test bypass callsite in the repo.
package brokerclienttest

import (
	"net/http"

	"github.com/tjorri/paddock/internal/brokerclient"
)

// NewUnchecked builds a brokerclient.Client that talks to httptest-
// server URLs (e.g. https://127.0.0.1:NNNNN) which the F-29 validator
// in brokerclient.New rejects. The supplied http.Client is used as the
// transport — typically srv.Client() from a httptest.Server.
//
// Production callers MUST NOT use this function. The package name is
// intentionally clumsy so a grep for `brokerclienttest` audits the
// callsites.
func NewUnchecked(opts brokerclient.Options, hc *http.Client) *brokerclient.Client {
	return brokerclient.NewForTest(opts, hc)
}
