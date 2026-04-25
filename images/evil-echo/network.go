package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

const dialTimeout = 5 * time.Second

func cmdConnectRawTCP(target string) Output {
	if target == "" {
		return Output{Flag: "--connect-raw-tcp", Result: "error", Error: "missing target"}
	}
	conn, err := net.DialTimeout("tcp", target, dialTimeout) //nolint:gosec,noctx
	if err != nil {
		return Output{Flag: "--connect-raw-tcp", Target: target, Result: "denied", Error: err.Error()}
	}
	_ = conn.Close()
	return Output{Flag: "--connect-raw-tcp", Target: target, Result: "success"}
}

func cmdConnectIPLiteral(target string) Output {
	// Same logic as connect-raw-tcp but the flag name signals to the test
	// reader that this specifically tests IP-literal CONNECT.
	out := cmdConnectRawTCP(target)
	out.Flag = "--connect-ip-literal"
	return out
}

func cmdConnectLoopback(port string) Output {
	if port == "" {
		return Output{Flag: "--connect-loopback", Result: "error", Error: "missing port"}
	}
	target := "127.0.0.1:" + port
	conn, err := net.DialTimeout("tcp", target, dialTimeout) //nolint:gosec,noctx
	if err != nil {
		return Output{Flag: "--connect-loopback", Target: target, Result: "denied", Error: err.Error()}
	}
	_ = conn.Close()
	return Output{Flag: "--connect-loopback", Target: target, Result: "success"}
}

func cmdProbeIMDS() Output {
	target := "169.254.169.254:80"
	conn, err := net.DialTimeout("tcp", target, dialTimeout) //nolint:gosec,noctx
	if err != nil {
		return Output{Flag: "--probe-imds", Target: target, Result: "denied", Error: err.Error()}
	}
	_ = conn.Close()
	return Output{Flag: "--probe-imds", Target: target, Result: "success"}
}

func cmdExfilViaDNS(host string) Output {
	if host == "" {
		return Output{Flag: "--exfil-via-dns", Result: "error", Error: "missing host"}
	}
	// A minimal DNS-exfil simulation: resolve the host. If the agent's
	// DNS allowlist is not honoured, the resolver call succeeds.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	resolver := net.Resolver{}
	_, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return Output{Flag: "--exfil-via-dns", Target: host, Result: "denied", Error: err.Error()}
	}
	return Output{Flag: "--exfil-via-dns", Target: host, Result: "success"}
}

func cmdProbeBroker(target string) Output {
	if target == "" {
		return Output{Flag: "--probe-broker", Result: "error", Error: "missing target"}
	}
	// Synthesise a bogus bearer in the broker's expected format
	// ("pdk-...") so the broker's validator at least gets to the
	// TokenReview step.
	syntheticBearer := "pdk-evil-echo-synthetic"
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr, Timeout: dialTimeout}
	req, _ := http.NewRequest("POST", target, nil) //nolint:gosec,noctx
	req.Header.Set("Authorization", "Bearer "+syntheticBearer)
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return Output{Flag: "--probe-broker", Target: target, Result: "denied", Error: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return Output{Flag: "--probe-broker", Target: target, Result: "success",
			Detail: map[string]any{"http_status": resp.StatusCode}}
	}
	return Output{Flag: "--probe-broker", Target: target, Result: "denied",
		Detail: map[string]any{"http_status": resp.StatusCode}}
}

func cmdFloodConnectRawTCP(target string) Output {
	if target == "" {
		return Output{Flag: "--flood-connect-raw-tcp", Result: "error", Error: "missing target"}
	}
	const attempts = 50
	successCount := 0
	var lastErr string
	for i := 0; i < attempts; i++ {
		conn, err := net.DialTimeout("tcp", target, dialTimeout) //nolint:gosec,noctx
		if err != nil {
			lastErr = err.Error()
			continue
		}
		successCount++
		_ = conn.Close()
	}
	if successCount == 0 {
		return Output{Flag: "--flood-connect-raw-tcp", Target: target, Result: "denied",
			Error:  lastErr,
			Detail: map[string]any{"attempts": attempts, "successes": successCount}}
	}
	return Output{Flag: "--flood-connect-raw-tcp", Target: target, Result: "success",
		Detail: map[string]any{"attempts": attempts, "successes": successCount}}
}

func cmdSmuggleHeaders(spec string) Output {
	// spec is "name=value"; we don't actually have a substitution-eligible
	// upstream to test against here. Mark as skipped — the e2e test
	// pipes this through the proxy and reads the proxy's audit log.
	if spec == "" {
		return Output{Flag: "--smuggle-headers", Result: "error", Error: "missing spec"}
	}
	// Attempt an HTTPS request to a placeholder upstream with the
	// smuggled header. The proxy is expected to scrub or proxy this.
	// In v0.4.1 there's no scrubbing — see F-21.
	target := os.Getenv("SMUGGLE_TARGET_URL")
	if target == "" {
		target = "https://example.com/"
	}
	req, _ := http.NewRequest("GET", target, nil) //nolint:gosec,noctx
	parts := splitOnce(spec, "=")
	if len(parts) != 2 {
		return Output{Flag: "--smuggle-headers", Result: "error", Error: "spec must be name=value"}
	}
	req.Header.Set(parts[0], parts[1])
	client := &http.Client{Timeout: dialTimeout}
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return Output{Flag: "--smuggle-headers", Target: target, Result: "denied", Error: err.Error()}
	}
	defer resp.Body.Close()
	return Output{Flag: "--smuggle-headers", Target: target, Result: "success",
		Detail: map[string]any{"http_status": resp.StatusCode, "header_name": parts[0]}}
}

func cmdProbeProviderSubstitutionHost(target string) Output {
	if target == "" {
		return Output{Flag: "--probe-provider-substitution-host", Result: "error", Error: "missing target"}
	}
	// Send a request to a non-allowlisted host with a known broker
	// bearer in Authorization. F-09: vertical providers don't host-scope
	// substitution, so the proxy might still substitute the real value.
	// E2E test asserts the request is denied (not silently substituted).
	bearer := os.Getenv("PADDOCK_TEST_BEARER")
	if bearer == "" {
		bearer = "pdk-anthropic-test-bearer"
	}
	req, _ := http.NewRequest("GET", target, nil) //nolint:gosec,noctx
	req.Header.Set("Authorization", "Bearer "+bearer)
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		DisableKeepAlives: true,
	}
	client := &http.Client{Transport: tr, Timeout: dialTimeout}
	resp, err := client.Do(req) //nolint:gosec
	if err != nil {
		return Output{Flag: "--probe-provider-substitution-host", Target: target, Result: "denied", Error: err.Error()}
	}
	defer resp.Body.Close()
	return Output{Flag: "--probe-provider-substitution-host", Target: target, Result: "success",
		Detail: map[string]any{"http_status": resp.StatusCode}}
}

// splitOnce splits s at the first occurrence of sep.
func splitOnce(s, sep string) []string {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return []string{s[:i], s[i+len(sep):]}
		}
	}
	return []string{s}
}

// cmdBypassProxyEnv unsets the proxy-related env vars in the current
// process. Subsequent flag handlers that rely on env vars (e.g., when
// the harness is run via cooperative HTTPS_PROXY) will see them absent.
// Returns Result: "success" because the unset itself is what we test;
// whether subsequent connections succeed is the proper assertion.
func cmdBypassProxyEnv() Output {
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		_ = os.Unsetenv(k)
	}
	return Output{Flag: "--bypass-proxy-env", Result: "success"}
}

// cmdProbeEnvOverride reports the values of the proxy-related env vars
// the agent sees. Test asserts the agent saw the controller-set
// HTTPS_PROXY (and not a tenant-overridden one).
func cmdProbeEnvOverride() Output {
	envs := map[string]string{
		"HTTPS_PROXY":         os.Getenv("HTTPS_PROXY"),
		"HTTP_PROXY":          os.Getenv("HTTP_PROXY"),
		"SSL_CERT_FILE":       os.Getenv("SSL_CERT_FILE"),
		"NODE_EXTRA_CA_CERTS": os.Getenv("NODE_EXTRA_CA_CERTS"),
	}
	return Output{Flag: "--probe-env-override", Result: "success", Detail: envs}
}

// Ensure fmt is used (it is used via http.NewRequest error path but compiler
// may not see it; add a compile-time reference).
var _ = fmt.Sprintf
