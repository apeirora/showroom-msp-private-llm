package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"testing"
)

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
