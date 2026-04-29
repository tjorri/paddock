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

// Package e2e — proxy MITM substitution regression test.
//
// Deploys an in-cluster TLS echo listener signed by a test CA, seeds
// the proxy's trust bundle with that CA, and asserts that the proxy
// substitutes the surrogate bearer for the real secret value before
// forwarding the request to the listener.
package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"paddock.dev/paddock/test/utils"
)

const (
	// subNS is the test namespace for the proxy-substitution spec.
	subNS = "paddock-test-substitution"
	// subTemplateName is the ClusterHarnessTemplate used in this spec.
	subTemplateName = "probe-curl-substitution"
	// listenerHost is the FQDN the proxy will connect to and substitute on.
	listenerHost = "probe-listener." + subNS + ".svc.cluster.local"
)

var _ = Describe("proxy MITM substitution", Ordered, func() {
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		sentinel string
		// dummyCAForRestore holds a freshly-generated dummy CA PEM so
		// AfterAll can restore the suite-level paddock-proxy-upstream-cas
		// ConfigMap after this Describe overwrites it with the test CA.
		dummyCAForRestore []byte
	)

	BeforeAll(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
		DeferCleanup(cancel)

		// 0. Generate a per-spec random sentinel so the final assertion
		//    is unambiguous — it can't match a cached response or the
		//    surrogate value that the proxy hands to the agent container.
		randBytes := make([]byte, 12)
		_, err := rand.Read(randBytes)
		Expect(err).NotTo(HaveOccurred(), "generating random sentinel bytes")
		sentinel = "PADDOCK-E2E-SENTINEL-" + strings.ToUpper(hex.EncodeToString(randBytes))
		By(fmt.Sprintf("sentinel value: %s", sentinel))

		// 1. Generate test CA + leaf cert for the in-cluster listener.
		By("generating test CA and leaf cert for the in-cluster listener")
		caPEM, leafPEM, leafKeyPEM, err := utils.GenerateCAAndLeaf(listenerHost)
		Expect(err).NotTo(HaveOccurred(), "GenerateCAAndLeaf for listener")

		// Pre-generate the dummy CA that AfterAll will restore so the
		// restore step has no dependency on test state that might be gone.
		dummyCA, _, _, err := utils.GenerateCAAndLeaf("paddock-e2e-dummy.invalid")
		Expect(err).NotTo(HaveOccurred(), "GenerateCAAndLeaf for restore dummy")
		dummyCAForRestore = dummyCA

		// 2. Re-create the test namespace cleanly so a previous failed
		//    run can't leave stale state that confuses assertions.
		By("re-creating the test namespace")
		runWithTimeout(30*time.Second, "kubectl", "delete", "ns", subNS,
			"--ignore-not-found=true", "--wait=true", "--timeout=30s")
		mustCreateNamespace(subNS)

		// 3. Apply the TLS Secret so the listener Pod can mount it.
		//    Secret.data requires base64-encoded values.
		By("applying the TLS Secret for the listener")
		tlsSecretYAML := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: probe-listener-tls
  namespace: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s
`, subNS,
			base64.StdEncoding.EncodeToString(leafPEM),
			base64.StdEncoding.EncodeToString(leafKeyPEM))
		mustApplyManifest(tlsSecretYAML)

		// 4. Overwrite paddock-proxy-upstream-cas with the test CA so
		//    the per-run proxy Pod trusts the listener's certificate.
		By("populating paddock-proxy-upstream-cas with the test CA")
		upstreamCMYAML := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
%s`, indent4(string(caPEM)))
		mustApplyManifest(upstreamCMYAML)

		// 5. Apply the listener Deployment + Service.
		// Adaptation C: container binds to 8443 (non-privileged); the
		// Service exposes 443 → 8443 so curl can reach it on the
		// standard HTTPS port without CAP_NET_BIND_SERVICE.
		By("deploying the probe-listener echo server")
		listenerYAML := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: probe-listener
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels: {app: probe-listener}
  template:
    metadata:
      labels: {app: probe-listener}
    spec:
      containers:
        - name: echo
          image: mendhak/http-https-echo:34
          env:
            - name: HTTPS_PORT
              value: "8443"
            - name: HTTP_PORT
              value: "8080"
            - name: HTTPS_CERT_FILE
              value: /app/cert.pem
            - name: HTTPS_KEY_FILE
              value: /app/key.pem
          volumeMounts:
            - name: tls
              mountPath: /app/cert.pem
              subPath: tls.crt
              readOnly: true
            - name: tls
              mountPath: /app/key.pem
              subPath: tls.key
              readOnly: true
          ports:
            - {containerPort: 8443, name: https}
      volumes:
        - name: tls
          secret:
            secretName: probe-listener-tls
---
apiVersion: v1
kind: Service
metadata:
  name: probe-listener
  namespace: %s
spec:
  selector: {app: probe-listener}
  ports:
    - {port: 443, targetPort: 8443, name: https}
`, subNS, subNS)
		mustApplyManifest(listenerYAML)

		// 6. Wait for the listener Deployment to roll out before
		//    submitting a HarnessRun that talks to it.
		By("waiting for probe-listener Deployment rollout")
		_, err = utils.Run(exec.CommandContext(ctx, "kubectl", "-n", subNS,
			"rollout", "status", "deploy/probe-listener", "--timeout=90s"))
		Expect(err).NotTo(HaveOccurred(), "probe-listener rollout")

		// 7. Apply the probe Secret carrying the real credential value
		//    (the sentinel). The broker will issue the surrogate bearer to
		//    the proxy; the proxy substitutes the surrogate for the
		//    sentinel on the wire.
		By("applying the probe Secret (real credential)")
		probeSecretYAML := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: probe-secret
  namespace: %s
stringData:
  token: %q
`, subNS, sentinel)
		mustApplyManifest(probeSecretYAML)

		// 8. Apply the BrokerPolicy granting PROBE_TOKEN via
		//    UserSuppliedSecret with proxyInjected delivery on
		//    listenerHost:443.
		By("applying the BrokerPolicy with UserSuppliedSecret + proxyInjected")
		bpYAML := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: BrokerPolicy
metadata:
  name: probe-substitution-policy
  namespace: %s
spec:
  appliesToTemplates: [%q]
  grants:
    credentials:
      - name: PROBE_TOKEN
        provider:
          kind: UserSuppliedSecret
          secretRef:
            name: probe-secret
            key: token
          deliveryMode:
            proxyInjected:
              hosts: [%q]
              header:
                name: Authorization
                valuePrefix: "Bearer "
    egress:
      - host: %s
        ports: [443]
`, subNS, subTemplateName, listenerHost, listenerHost)
		mustApplyManifest(bpYAML)

		// 9. Apply the ClusterHarnessTemplate: a minimal curl agent that
		//    sends PROBE_TOKEN (as seen by the container — the surrogate)
		//    in an Authorization header to the listener. The proxy is
		//    expected to swap the surrogate for the real sentinel before
		//    the request leaves the proxy.
		By("applying the ClusterHarnessTemplate (curl agent)")
		chtYAML := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: ClusterHarnessTemplate
metadata:
  name: %s
spec:
  harness: probe-curl
  image: curlimages/curl:8.10.1
  command: ["sh", "-c"]
  args:
    - |
      curl -sS -H "Authorization: Bearer $PROBE_TOKEN" https://%s/anything
  requires:
    credentials:
      - name: PROBE_TOKEN
    egress:
      - host: %s
        ports: [443]
  defaults:
    timeout: 120s
    resources:
      limits:
        cpu: 200m
        memory: 128Mi
      requests:
        cpu: 50m
        memory: 64Mi
  workspace:
    required: true
    mountPath: /workspace
`, subTemplateName, listenerHost, listenerHost)
		mustApplyManifest(chtYAML)
	})

	AfterAll(func() {
		if os.Getenv("KEEP_E2E_RUN") == "1" {
			return
		}

		// Delete the test namespace (listener, probe Secret, BrokerPolicy,
		// HarnessRun, Workspace all live here).
		runWithTimeout(10*time.Second, "kubectl", "delete", "ns", subNS,
			"--wait=false", "--ignore-not-found=true")
		if !waitForNamespaceGone(subNS, 120*time.Second) {
			fmt.Fprintf(GinkgoWriter,
				"WARNING: namespace %s stuck in Terminating after 120s; "+
					"controller-side finalizer drain likely broken — force-clearing\n", subNS)
			forceClearFinalizers(subNS)
			waitForNamespaceGone(subNS, 20*time.Second)
		}

		// Delete the cluster-scoped ClusterHarnessTemplate.
		runWithTimeout(10*time.Second, "kubectl", "delete", "clusterharnesstemplate",
			subTemplateName, "--ignore-not-found=true")

		// Restore the suite-level dummy paddock-proxy-upstream-cas
		// ConfigMap so Describes that run after this one still have a
		// valid (though non-functional) CA bundle mounted by per-run
		// proxy Pods.
		if dummyCAForRestore != nil {
			restoreCMYAML := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: paddock-proxy-upstream-cas
  namespace: paddock-system
data:
  bundle.pem: |
%s`, indent4(string(dummyCAForRestore)))
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(restoreCMYAML)
			_, _ = utils.Run(cmd)
		}
	})

	It("substitutes the surrogate bearer for the real secret value at the proxy", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		runName := "probe-substitution-run"

		// Adaptation B: dumpRunDiagnostics returns nothing, so register
		// it via DeferCleanup gated on CurrentSpecReport().Failed() —
		// mirrors the pattern used in hostile_test.go:776.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpRunDiagnostics(ctx, subNS, runName)
			}
		})

		By("applying the HarnessRun")
		// Adaptation A: mustApplyManifest (from hostile_test.go:1492)
		// reused directly; no applyManifest(ctx, ...) variant needed.
		runYAML := fmt.Sprintf(`
apiVersion: paddock.dev/v1alpha1
kind: HarnessRun
metadata:
  name: %s
  namespace: %s
spec:
  templateRef:
    name: %s
    kind: ClusterHarnessTemplate
  prompt: "proxy-substitution e2e"
`, runName, subNS, subTemplateName)
		mustApplyManifest(runYAML)

		By("waiting for the HarnessRun to reach Succeeded")
		Eventually(func() string {
			return runPhase(ctx, subNS, runName)
		}, 4*time.Minute, 5*time.Second).Should(Equal("Succeeded"))

		By("asserting the listener received the substituted real secret value")
		out := readRunOutput(ctx, subNS, runName)

		// Adaptation D: substring match on "Bearer <sentinel>" — robust
		// to header-key casing in the Node-based echo server's JSON
		// response (headers are typically lowercased) and to any shape
		// changes in the mendhak/http-https-echo image output.
		Expect(out).To(ContainSubstring("Bearer "+sentinel),
			"proxy should have substituted the surrogate bearer for the real sentinel; agent stdout:\n%s", out)
	})
})
