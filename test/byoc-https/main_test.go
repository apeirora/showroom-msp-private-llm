package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPlaintextProbeReachesConfiguredHostname(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantError bool
	}{
		{http.StatusOK, true}, {http.StatusFound, true}, {http.StatusUnauthorized, true},
		{http.StatusForbidden, true}, {http.StatusMethodNotAllowed, true},
		{http.StatusInternalServerError, true}, {http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false}, {http.StatusNotFound, false},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			var matched atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != hostname || r.URL.Path != "/v1/models" {
					http.NotFound(w, r)
					return
				}
				matched.Store(true)
				if r.Header.Get("Authorization") != "" {
					t.Error("plaintext probe sent credentials")
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			f := &fixture{address: strings.TrimPrefix(server.URL, "http://"), report: &report{}}
			err := f.testPlaintext()
			if !matched.Load() {
				t.Fatal("plaintext probe bypassed the configured hostname route")
			}
			if tc.wantError && (err == nil || !strings.Contains(err.Error(), "plaintext accepted")) {
				t.Fatalf("reachable plaintext route was not detected: status=%d err=%v", tc.status, err)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("plaintext protocol/no-route rejection failed probe: %v", err)
			}
		})
	}
}

func TestPlaintextProbeAllowsTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := strings.TrimPrefix(server.URL, "http://")
	server.Close()
	f := &fixture{address: address, report: &report{}}
	if err := f.testPlaintext(); err != nil {
		t.Fatalf("closed HTTP listener did not qualify as transport rejection: %v", err)
	}
}

func TestMissingSNIRequiresServerAlert(t *testing.T) {
	alert := &net.OpError{Op: "remote error", Err: errors.New("tls: unrecognized name")}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"server alert", alert, true},
		{"wrapped server alert", fmt.Errorf("TLS handshake: %w", alert), true},
		{"successful handshake", nil, false},
		{"local error text", errors.New("tls: unrecognized name"), false},
		{"other server alert", &net.OpError{Op: "remote error", Err: errors.New("tls: handshake failure")}, false},
		{"network failure", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, false},
		{"untrusted chain", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, false},
		{"wrong hostname", &tls.CertificateVerificationError{Err: x509.HostnameError{}}, false},
		{"expired certificate",
			&tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingSNIAlert(tc.err); got != tc.want {
				t.Fatalf("classified missing-SNI rejection as %t, want %t", got, tc.want)
			}
		})
	}
}
