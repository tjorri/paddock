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

// Package auditing is the single source of truth for AuditEvent
// emission across broker, proxy, webhook, and controller. It exports a
// Sink interface, a KubeSink that calls client.Create, and per-kind
// builder functions. Callers decide fail-closed vs fail-open on a
// Sink.Write error; see Phase 2c spec §3.1 for the policy table.
package auditing

import "errors"

// ErrAuditWrite wraps every Sink.Write failure. Callers test with
// errors.Is so a transport switch (etcd → Loki, hypothetically) doesn't
// require call-site changes.
var ErrAuditWrite = errors.New("audit write failed")
