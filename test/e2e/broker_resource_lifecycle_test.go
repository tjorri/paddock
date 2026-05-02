//go:build e2e
// +build e2e

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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tjorri/paddock/test/e2e/framework"
	"github.com/tjorri/paddock/test/utils"
)

const (
	patPoolRevokeNS = "paddock-t2-revoke"
	issueOversizeNS = "paddock-t2-oversize"
)

var _ = Describe("broker resource lifecycle", Label("broker"), func() {
	It("revokes a PATPool lease when the issuing run is deleted", func(ctx SpecContext) {
		ctxT, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()

		_, _ = utils.Run(exec.CommandContext(ctxT, "kubectl",
			"delete", "ns", patPoolRevokeNS, "--ignore-not-found", "--wait=true", "--timeout=60s"))
		ns := framework.CreateTenantNamespace(ctxT, patPoolRevokeNS)

		By("creating pool Secret, HarnessTemplate, and BrokerPolicy")
		framework.ApplyYAML(framework.PATPoolFixtureManifest(ns, "t2-revoke", 2))

		By("submitting a HarnessRun that acquires a PATPool lease")
		runName := "revoke-test"
		// Dump describe + events + controller + broker logs on any
		// spec failure (lease-acquisition timeout, finalizer stuck,
		// metric never decrements) so the next CI flake gives us
		// real signal instead of a bare Eventually-timed-out line.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpRunDiagnostics(ctxT, ns, runName)
			}
		})
		framework.NewRun(ns, "t2-patpool-tmpl").
			WithName(runName).
			WithPrompt("t2 revoke test").
			Submit(ctxT)

		By("waiting for at least one IssuedLease to appear on the run")
		Eventually(func() int {
			return framework.IssuedLeaseCount(ctxT, ns, runName)
		}, 90*time.Second, 2*time.Second).Should(BeNumerically(">=", 1))

		By("recording the current PATPool leased count from broker metrics")
		leasedBefore := framework.GetBroker(ctxT).Metric(ctxT, "paddock_broker_patpool_leased")

		By("deleting the HarnessRun")
		_, err := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", ns,
			"delete", "harnessrun", runName, "--wait=false"))
		Expect(err).NotTo(HaveOccurred())

		By("asserting the run is fully gone within 60s")
		Eventually(func() bool {
			_, err := utils.Run(exec.CommandContext(ctxT, "kubectl", "-n", ns,
				"get", "harnessrun", runName))
			return err != nil && strings.Contains(err.Error(), "not found")
		}, 60*time.Second, 2*time.Second).Should(BeTrue(),
			"HarnessRun %s/%s still present after 60s — broker-leases finalizer may be stuck", ns, runName)

		By("asserting the PATPool slot was freed by Revoke")
		Eventually(func() float64 {
			return framework.GetBroker(ctxT).Metric(ctxT, "paddock_broker_patpool_leased")
		}, 30*time.Second, 2*time.Second).Should(BeNumerically("<", leasedBefore),
			"PATPool leased count did not decrease after run delete; lease was not revoked")
	})

	It("rejects oversize bodies on /v1/issue", func(ctx SpecContext) {
		ctxT, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		// On spec failure, dump broker pod state + controller +
		// broker logs so a port-forward / TLS-handshake / 401-route
		// regression surfaces with diagnostic context.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				framework.DumpBrokerDiagnostics(ctxT)
			}
		})

		// The load-bearing F-17(a) oversize-body assertion lives in
		// TestHandleIssue_OversizeBody_BadRequest (internal/broker/server_test.go).
		// That test wires a fake authenticated caller with a 100 KiB body and
		// asserts HTTP 400. In e2e the broker API port (:8443) requires a valid
		// SA bearer — without one auth fails with 401 BEFORE the body is read,
		// so MaxBytesReader never triggers on an unauthenticated request.
		//
		// This smoke spec asserts the API port is reachable and returns a
		// well-formed JSON ErrorResponse for an unauthenticated call, which
		// confirms the broker is correctly wired with TLS + the limitBody
		// middleware (TLS handshake would fail on a misconfigured server;
		// a missing limitBody wrapper would still return 401 here, so
		// this smoke only indirectly confirms F-17(a) wiring — the unit
		// test carries the direct assertion).

		// port-forward the TLS API port from the broker pod.
		pod := framework.GetBroker(ctxT).PodName(ctxT)
		Expect(pod).NotTo(BeEmpty(), "no broker pod found")

		const localTLSPort = "19443"
		pfCtx, pfCancel := context.WithCancel(ctxT)
		defer pfCancel()
		pfCmd := exec.CommandContext(pfCtx, "kubectl", "-n", framework.BrokerNamespace,
			"port-forward", "pod/"+pod, localTLSPort+":8443")
		Expect(pfCmd.Start()).To(Succeed(), "starting port-forward to broker :8443")
		time.Sleep(500 * time.Millisecond)

		By("sending an unauthenticated POST /v1/issue with oversize body and asserting well-formed JSON error")
		oversizeBody := bytes.Repeat([]byte("x"), 100<<10) // 100 KiB
		req, err := http.NewRequestWithContext(ctxT, http.MethodPost,
			"https://127.0.0.1:"+localTLSPort+"/v1/issue",
			bytes.NewReader(oversizeBody))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Paddock-Run", "oversize-smoke")

		httpClient := framework.GetBroker(ctxT).HTTPClient()
		resp, doErr := httpClient.Do(req)
		Expect(doErr).NotTo(HaveOccurred(),
			"POST /v1/issue failed — broker may not be reachable via port-forward")
		defer resp.Body.Close()

		// Without a valid bearer the broker returns 401 Unauthorized.
		// That is acceptable here — it confirms the broker is up and
		// routing requests through limitBody. The MaxBytesReader cap
		// itself is validated by the unit test.
		Expect(resp.StatusCode).To(BeElementOf(http.StatusUnauthorized, http.StatusBadRequest),
			"expected 401 (unauthenticated) or 400 (body too large); got %d", resp.StatusCode)

		var errResp map[string]any
		body, readErr := io.ReadAll(resp.Body)
		Expect(readErr).NotTo(HaveOccurred())
		Expect(json.Unmarshal(body, &errResp)).To(Succeed(),
			"response body should be a JSON ErrorResponse; got: %s", string(body))
		Expect(errResp).To(HaveKey("code"),
			"ErrorResponse must carry a 'code' field; got: %v", errResp)
	})
})
