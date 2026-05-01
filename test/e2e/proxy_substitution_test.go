//go:build e2e

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

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/utils"
)

// Regression for #83: handleValidateEgress must derive SubstituteAuth
// from credential grants so the proxy MITMs the request and swaps the
// agent's pdk-usersecret-* surrogate for the real value.
//
// Probe shape mirrors the issue's manual repro fixture: a public echo
// service (httpbin.org/anything) returns the request as JSON in its
// response body, the agent's curl writes that JSON to stdout, and the
// assertion checks that the Authorization header httpbin received
// carries the per-run real-secret sentinel — not the surrogate.
//
// This is not hermetic (depends on httpbin.org reachability), but it
// closes the e2e gap documented at hostile_test.go:740-742 that allowed
// #83 to ship undetected. A future fully-hermetic regression requires
// replicating the upstream-CA bundle into per-run namespaces; deferred
// to a follow-up.
var _ = Describe("proxy MITM substitution", Ordered, func() {
	const (
		subNS           = "paddock-test-substitution"
		subTemplateName = "probe-curl"
		listenerHost    = "httpbin.org"
	)
	var sentinel string

	BeforeAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Per-test-run random sentinel so the assertion can't accidentally
		// match leftover state from a previous run.
		buf := make([]byte, 8)
		_, err := rand.Read(buf)
		Expect(err).NotTo(HaveOccurred(), "rand.Read for sentinel")
		sentinel = "PADDOCK-E2E-SENTINEL-" + hex.EncodeToString(buf)

		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", subNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		mustCreateNamespace(subNS)

		mustApplyManifest(fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: probe-secret
  namespace: %s
stringData:
  token: %q`, subNS, sentinel))

		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: probe-policy
  namespace: %s
spec:
  appliesToTemplates: ["%s"]
  grants:
    credentials:
      - name: PROBE_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef: {name: probe-secret, key: token}
          deliveryMode:
            proxyInjected:
              hosts: [%q]
              header: {name: Authorization, valuePrefix: "Bearer "}
    egress:
      - host: %q
        ports: [443]`, subNS, subTemplateName, listenerHost, listenerHost))

		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  harness: %s
  image: curlimages/curl:8.10.1
  command: ["sh", "-c"]
  args:
    - |
      curl -sS -H "Authorization: Bearer $PROBE_TOKEN" https://%s/anything
  requires:
    credentials:
      - name: PROBE_TOKEN
    egress:
      - host: %q
        ports: [443]
  defaults:
    timeout: 90s
  workspace:
    required: true
    mountPath: /workspace`, subTemplateName, subTemplateName, listenerHost, listenerHost))
	})

	AfterAll(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "ns", subNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		_, _ = utils.Run(exec.CommandContext(ctx, "kubectl",
			"delete", "clusterharnesstemplate", subTemplateName, "--ignore-not-found"))
	})

	It("substitutes a credential into requests addressed to a public host", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		runName := "substitute-probe"
		mustApplyManifest(fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: ClusterHarnessTemplate
  prompt: "probe-substitution"`, runName, subNS, subTemplateName))

		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpRunDiagnostics(ctx, subNS, runName)
			}
		})

		Eventually(func() string {
			return runPhase(ctx, subNS, runName)
		}, 4*time.Minute, 5*time.Second).Should(Equal("Succeeded"))

		// httpbin.org/anything echoes the request as JSON in the response
		// body. The agent's curl writes that JSON to stdout. The
		// substituted Authorization header carries the real sentinel; if
		// substitution didn't fire, the agent's pdk-usersecret-* surrogate
		// would be there instead.
		//
		// Substring match (rather than JSON parse + headers["Authorization"])
		// is robust to header-key casing and to httpbin's response-shape
		// drift; the per-run random sentinel makes false positives
		// effectively impossible.
		out := readRunOutput(ctx, subNS, runName)
		want := "Bearer " + sentinel
		Expect(out).To(ContainSubstring(want),
			"agent stdout should contain the substituted real-secret sentinel (httpbin echoes the request); proxy may not have MITMed.\nexpected: %q\nagent stdout:\n%s",
			want, out)
	})
})
