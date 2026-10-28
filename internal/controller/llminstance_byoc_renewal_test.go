package controller

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const renewalTimestampField = "ensureRenewedAfter"

func TestBYOCTLSManualRenewalLifecycle(t *testing.T) {
	ctx := context.Background()
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, hostname, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, oldRevision := ca.issue(t, hostname, nil)
	source := addBYOCSource(t, r, inst, cfg, cert, data)
	if revision, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || revision != oldRevision {
		t.Fatalf("initial certificate delivery failed: %v", err)
	}

	// Exercise Gardener's request -> acknowledgement -> Ready sequence. Both
	// generations awaiting issuance must keep the valid serving certificate.
	spec := cert.Object["spec"].(map[string]interface{})
	spec["renew"] = true
	cert.SetGeneration(2)
	cert.Object["status"] = map[string]interface{}{"state": "Pending", "observedGeneration": int64(1)}
	for _, phase := range []string{"request", "acknowledgement", "issued"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "acknowledgement" {
				delete(spec, "renew")
				spec[renewalTimestampField] = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
				cert.SetGeneration(3)
				cert.Object["status"] = map[string]interface{}{"state": "Pending", "observedGeneration": int64(2)}
			}
			wantRevision := oldRevision
			if phase == "issued" {
				source.Data, wantRevision = ca.issue(t, hostname, nil)
				if err := r.Update(ctx, source); err != nil {
					t.Fatal(err)
				}
				cert.Object["status"] = map[string]interface{}{"state": byocCertificateReadyState, "observedGeneration": int64(3)}
				if wantRevision == oldRevision {
					t.Fatal("replacement certificate did not change its fingerprint")
				}
			}
			if err := r.Update(ctx, cert); err != nil {
				t.Fatal(err)
			}
			before := cert.DeepCopy()
			for range 2 {
				if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); err != nil {
					t.Fatal(err)
				}
				if cert.GetResourceVersion() != before.GetResourceVersion() || !reflect.DeepEqual(cert.Object["spec"], before.Object["spec"]) {
					t.Fatal("reconciliation rewrote Gardener's renewal controls")
				}
				if revision, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || revision != wantRevision {
					t.Fatalf("renewal delivery/fallback failed: %v", err)
				}
			}
			// Get replaces the unstructured map; use the current value next time.
			spec = cert.Object["spec"].(map[string]interface{})
		})
	}
}

func TestBYOCTLSManualRenewalRejectsMalformedControls(t *testing.T) {
	cases := []struct {
		name, field string
		value       interface{}
	}{
		{"string boolean", "renew", "true"},
		{"numeric boolean", "renew", int64(1)},
		{"object boolean", "renew", map[string]interface{}{"value": true}},
		{"boolean timestamp", renewalTimestampField, true},
		{"numeric timestamp", renewalTimestampField, int64(123)},
		{"invalid timestamp", renewalTimestampField, "not-a-timestamp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
			_, hostname, _ := byocTLSIdentity(inst, cfg, "target-a")
			data, _ := ca.issue(t, hostname, nil)
			addBYOCSource(t, r, inst, cfg, cert, data)
			cert.Object["spec"].(map[string]interface{})[tc.field] = tc.value
			if err := r.Update(ctx, cert); err != nil {
				t.Fatal(err)
			}
			if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err == nil {
				t.Fatal("accepted malformed renewal control during reconciliation")
			}
			if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err == nil {
				t.Fatal("accepted malformed renewal control during delivery")
			}
			var output corev1.Secret
			if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: cert.GetName()}, &output); !apierrors.IsNotFound(err) {
				t.Fatalf("malformed control permitted remote TLS delivery: %v", err)
			}
		})
	}
}

func TestBYOCTLSManualRenewalPreservesOnlyRenewalControls(t *testing.T) {
	for _, field := range []string{"commonName", "csr", "privateKey", "renewBefore", "dnsNames", "issuerRef", "secretRef", "secretLabels"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
			_, hostname, _ := byocTLSIdentity(inst, cfg, "target-a")
			data, revision := ca.issue(t, hostname, nil)
			addBYOCSource(t, r, inst, cfg, cert, data)
			spec := cert.Object["spec"].(map[string]interface{})
			spec["renew"] = true
			spec[field] = "foreign-override"
			if err := r.Update(ctx, cert); err != nil {
				t.Fatal(err)
			}
			if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err == nil {
				t.Fatal("renewal control allowed an unconverged certificate specification")
			}
			if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); err != nil {
				t.Fatal(err)
			}
			want := byocCertificateSpec(cert.GetName(), hostname, string(cert.GetUID()), cfg, inst)
			want["renew"] = true
			if !reflect.DeepEqual(cert.Object["spec"], want) {
				t.Fatal("preserved an operator-owned specification override")
			}
			if got, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || got != revision {
				t.Fatalf("corrected specification did not allow valid delivery: %v", err)
			}
		})
	}
}

func TestBYOCTLSManualRenewalOptionalValues(t *testing.T) {
	for _, value := range []interface{}{nil, false} {
		cert := &unstructured.Unstructured{Object: map[string]interface{}{"spec": map[string]interface{}{"renew": value, renewalTimestampField: nil}}}
		desired := map[string]interface{}{}
		if err := preserveBYOCCertificateRenewal(cert, desired); err != nil {
			t.Fatal(err)
		}
		if _, found := desired[renewalTimestampField]; found {
			t.Fatal("preserved a cleared renewal timestamp")
		}
		if got, found := desired["renew"]; value == nil && found || value != nil && (!found || got != false) {
			t.Fatal("optional renewal boolean changed")
		}
	}
}
