package egress

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicEgressTimeLimits(t *testing.T) {
	if siteTimeout != 5*time.Second || MaxCheckTime != 15*time.Second {
		t.Fatalf("website limit = %v, family limit = %v", siteTimeout, MaxCheckTime)
	}
}

func TestPublicEgressRouteTableConservative(t *testing.T) {
	const header = "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"
	for _, tc := range []struct {
		name, family, table string
		empty               bool
	}{
		{"malformed IPv6 row", "ipv6", "malformed row\n", false},
		{"IPv4 with one route", "ipv4", header + "eth0 00000000 010200C0 0003 0 0 100 00000000 0 0 0\n", false},
		{"empty IPv6", "ipv6", "", true},
		{"IPv4 header only", "ipv4", header, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeTableEmpty(tc.table, tc.family); got != tc.empty {
				t.Fatalf("routeTableEmpty = %t, want %t", got, tc.empty)
			}
		})
	}
}

func TestPublicEgressIgnoresEnvironmentProxy(t *testing.T) {
	const isolated = "PROBE_TEST_EGRESS_PROXY_CHILD"
	// ProxyFromEnvironment caches the environment on first use in each process.
	if os.Getenv(isolated) != "1" {
		t.Setenv(isolated, "1")
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPublicEgressIgnoresEnvironmentProxy$")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated proxy test: %v\n%s", err, output)
		}
		return
	}
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	t.Setenv("REQUEST_METHOD", "")

	for i, current := range NewChecker().sites {
		t.Run(current.name, func(t *testing.T) {
			bodies := []string{`{"ip":"8.8.4.4"}`, "8.8.4.4\n", "ip=8.8.4.4\n"}
			checker := controlledChecker(t, "ipv4", func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, bodies[i])
			})
			resolve := checker.resolver
			checker.resolver = func(ctx context.Context, family, host string) ([]net.IP, error) {
				if host == "127.0.0.1" {
					return []net.IP{net.IPv4(127, 0, 0, 1)}, nil
				}
				return resolve(ctx, family, host)
			}
			current.endpoint = checker.sites[0].endpoint
			checker.sites[0] = current
			before := proxyRequests.Load()
			got := checker.Check(context.Background(), "ipv4")
			if hits := proxyRequests.Load() - before; hits != 0 {
				t.Errorf("proxy received %d requests", hits)
			}
			if got.Site != current.name || got.IP != "8.8.4.4" {
				t.Errorf("expected verified address from %s, got %+v", current.name, got)
			}
		})
	}
}

func TestPublicEgressResponseBodyLimit(t *testing.T) {
	for i, current := range NewChecker().sites {
		t.Run(current.name, func(t *testing.T) {
			for _, size := range []int{1024, 1025} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					prefixes := []string{`{"ip":"8.8.4.4"}`, "8.8.4.4", "ip=8.8.4.4\nunused="}
					prefix := prefixes[i]
					body := append([]byte(prefix), bytes.Repeat([]byte(" "), size-len(prefix))...)
					selected := current
					if i == 1 {
						// Native icanhazip text is one IP and an optional newline.
						// Remove fixture padding only for this size-limit test.
						selected.parse = func(data []byte) string { return current.parse(bytes.TrimRight(data, " ")) }
					}
					if ip := selected.parse(body); ip != "8.8.4.4" {
						t.Fatalf("fixture must parse before size validation, got %q", ip)
					}
					checker := controlledChecker(t, "ipv4", func(w http.ResponseWriter, _ *http.Request) {
						if i == 2 {
							w.WriteHeader(http.StatusOK)
							w.(http.Flusher).Flush() // HTTP/1.1 chunked, no Content-Length.
						}
						_, _ = w.Write(body)
					})
					selected.endpoint = checker.sites[0].endpoint
					checker.sites[0] = selected
					got := checker.Check(context.Background(), "ipv4")
					if size == 1024 && got.IP != "8.8.4.4" || size > 1024 && (got.Status != "unknown" || got.Reason != "invalid_response") {
						t.Fatalf("result for %d bytes: %+v", size, got)
					}
				})
			}
		})
	}
}
