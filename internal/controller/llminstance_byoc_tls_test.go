package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type byocTLSTestReporter interface {
	Helper()
	Fatal(...interface{})
}

type byocTLSTestAuthority struct {
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
	roots *x509.CertPool
}

func newBYOCTLSTestAuthority(t byocTLSTestReporter) byocTLSTestAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return byocTLSTestAuthority{cert, key, roots}
}
func (ca byocTLSTestAuthority) issue(t byocTLSTestReporter, hostname string, mutate func(*x509.Certificate)) (map[string][]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, DNSNames: []string{hostname}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if mutate != nil {
		mutate(template)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return map[string][]byte{corev1.TLSCertKey: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}, hex.EncodeToString(sum[:])
}
func byocTLSFixture(t *testing.T) (*LLMInstanceReconciler, client.Client, *llmv1alpha1.LLMInstance, BYOCTLSConfig, *unstructured.Unstructured, byocTLSTestAuthority) {
	t.Helper()
	ca := newBYOCTLSTestAuthority(t)
	cfg := BYOCTLSConfig{Domain: "byoc.example.test", Namespace: "tls-system", IssuerName: "issuer", IssuerNamespace: "kube-system", DNSClass: "garden", roots: ca.roots}
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "model", UID: types.UID("f2c64b38-9524-4752-9cf0-768acba8699f")}}
	name, host, err := byocTLSIdentity(inst, cfg, "target-a")
	if err != nil {
		t.Fatal(err)
	}
	cert := byocTLSObject(byocCertificateGVK, cfg.Namespace, name)
	cert.SetUID("certificate-uid")
	cert.SetGeneration(1)
	cert.SetLabels(map[string]string{byocTLSOwnerLabel: string(inst.UID)})
	cert.Object["spec"] = byocCertificateSpec(name, host, string(cert.GetUID()), cfg, inst)
	cert.Object["status"] = map[string]interface{}{"state": byocCertificateReadyState, "observedGeneration": int64(1)}
	scheme := runtime.NewScheme()
	if err = corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cert).Build()
	remote := fake.NewClientBuilder().WithScheme(scheme).Build()
	return &LLMInstanceReconciler{Client: local, Scheme: scheme}, remote, inst, cfg, cert, ca
}
func addBYOCSource(t *testing.T, r *LLMInstanceReconciler, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, cert *unstructured.Unstructured, data map[string][]byte) *corev1.Secret {
	t.Helper()
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cfg.Namespace, Name: cert.GetName(), Labels: map[string]string{byocTLSOwnerLabel: string(inst.UID), byocTLSCertificateLabel: string(cert.GetUID())}}, Type: corev1.SecretTypeTLS, Data: copyBYOCTLSData(data)}
	if err := r.Create(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	return source
}
func TestBYOCTLSIdentityUsesInstanceAndTarget(t *testing.T) {
	_, _, inst, cfg, _, _ := byocTLSFixture(t)
	name, host, err := byocTLSIdentity(inst, cfg, "target-a")
	if err != nil {
		t.Fatal(err)
	}
	inst.Annotations = map[string]string{slugAnnotationKey: "changed"}
	second, _, _ := byocTLSIdentity(inst, cfg, "target-a")
	if second != name {
		t.Fatal("tenant slug changed TLS identity")
	}
	replacement, _, _ := byocTLSIdentity(inst, cfg, "target-b")
	if replacement == name {
		t.Fatal("target replacement reuses old certificate SAN")
	}
	if host != name+"."+cfg.Domain {
		t.Fatal("unexpected hostname")
	}
	for _, bad := range []string{"", "*.example.test", "example.test/path"} {
		broken := cfg
		broken.Domain = bad
		if _, _, err = byocTLSIdentity(inst, broken, "target-a"); err == nil {
			t.Fatalf("accepted domain %q", bad)
		}
	}
	cfg.Namespace = inst.Namespace
	if _, _, err = byocTLSIdentity(inst, cfg, "target-a"); err == nil {
		t.Fatal("accepted tenant TLS source namespace")
	}
}
func TestBYOCTLSCertificateValidation(t *testing.T) {
	ca := newBYOCTLSTestAuthority(t)
	hostname := "instance.byoc.example.test"
	data, expected := ca.issue(t, hostname, nil)
	if got, err := validateBYOCTLSMaterial(data, hostname, time.Now(), ca.roots); err != nil || got != expected {
		t.Fatalf("valid leaf rejected: %v", err)
	}
	cases := map[string]func(*x509.Certificate){
		"expired":        func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) },
		"expiry margin":  func(c *x509.Certificate) { c.NotAfter = time.Now().Add(time.Minute) },
		"future":         func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Minute) },
		"wrong hostname": func(c *x509.Certificate) { c.DNSNames = []string{"other.example.test"} },
		"wildcard":       func(c *x509.Certificate) { c.DNSNames = []string{"*.byoc.example.test"} },
		"extra SAN":      func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "another.example.test") },
		"no serverAuth":  func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} },
		"CA":             func(c *x509.Certificate) { c.IsCA = true; c.BasicConstraintsValid = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad, _ := ca.issue(t, hostname, mutate)
			if _, err := validateBYOCTLSMaterial(bad, hostname, time.Now(), ca.roots); err == nil {
				t.Fatal("invalid certificate accepted")
			}
		})
	}
	other, _ := ca.issue(t, hostname, nil)
	mismatch := copyBYOCTLSData(data)
	mismatch[corev1.TLSPrivateKeyKey] = other[corev1.TLSPrivateKeyKey]
	if _, err := validateBYOCTLSMaterial(mismatch, hostname, time.Now(), ca.roots); err == nil {
		t.Fatal("mismatched private key accepted")
	}
	if _, err := validateBYOCTLSMaterial(data, hostname, time.Now(), x509.NewCertPool()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
}
func TestBYOCTLSSourceAndRemoteOwnership(t *testing.T) {
	for _, side := range []string{"source", "remote"} {
		t.Run(side, func(t *testing.T) {
			r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
			_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
			data, _ := ca.issue(t, host, nil)
			source := addBYOCSource(t, r, inst, cfg, cert, data)
			if side == "source" {
				source.Labels[byocTLSCertificateLabel] = "wrong-uid"
				if err := r.Update(context.Background(), source); err != nil {
					t.Fatal(err)
				}
			} else {
				foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: cert.GetName()}, Data: map[string][]byte{"preserve": []byte("unrelated")}}
				if err := remote.Create(context.Background(), foreign); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.reconcileBYOCTLSSecret(context.Background(), remote, inst, cfg, "target-a"); err == nil {
				t.Fatal("foreign Secret accepted")
			}
			var got corev1.Secret
			err := remote.Get(context.Background(), client.ObjectKey{Namespace: byocNamespace, Name: cert.GetName()}, &got)
			if side == "source" && !apierrors.IsNotFound(err) {
				t.Fatal("foreign source was delivered")
			}
			if side == "remote" && (err != nil || string(got.Data["preserve"]) != "unrelated") {
				t.Fatal("foreign remote Secret mutated")
			}
		})
	}
}
func TestBYOCTLSRenewalRetainsProtectedCache(t *testing.T) {
	ctx := context.Background()
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, first := ca.issue(t, host, nil)
	data["provider-password"] = []byte("must-not-copy")
	source := addBYOCSource(t, r, inst, cfg, cert, data)
	if got, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || got != first {
		t.Fatalf("initial delivery failed: %v", err)
	}
	var destination corev1.Secret
	key := client.ObjectKey{Namespace: byocNamespace, Name: cert.GetName()}
	if err := remote.Get(ctx, key, &destination); err != nil {
		t.Fatal(err)
	}
	if len(destination.Data) != 2 {
		t.Fatal("extra source secret data was exported")
	}
	// Corruption in the tenant cluster is repaired from the protected MSP copy.
	destination.Data[corev1.TLSPrivateKeyKey] = []byte("tenant supplied")
	if err := remote.Update(ctx, &destination); err != nil {
		t.Fatal(err)
	}
	source.Data[corev1.TLSPrivateKeyKey] = []byte("pending renewal mismatch")
	if err := r.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	if got, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || got != first {
		t.Fatalf("last valid cache was not retained: %v", err)
	}
	if err := remote.Get(ctx, key, &destination); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destination.Data[corev1.TLSPrivateKeyKey], data[corev1.TLSPrivateKeyKey]) {
		t.Fatal("tenant material was trusted")
	}
	renewed, second := ca.issue(t, host, nil)
	source.Data = renewed
	if err := r.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	if got, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || got != second || got == first {
		t.Fatalf("renewal did not change rollout fingerprint: %v", err)
	}
	// A pending issuer state may keep a still-valid cache, but expiration cannot.
	cert.Object["status"] = map[string]interface{}{"state": "Pending", "observedGeneration": int64(1)}
	if err := r.Update(ctx, cert); err != nil {
		t.Fatal(err)
	}
	if got, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil || got != second {
		t.Fatalf("pending renewal discarded valid cache: %v", err)
	}
	var cache corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: cert.GetName() + "-last-valid"}, &cache); err != nil {
		t.Fatal(err)
	}
	cache.Data, _ = ca.issue(t, host, func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) })
	if err := r.Update(ctx, &cache); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("expired cache remained ready: %v", err)
	}
}
func TestBYOCTLSRejectsRevokedMaterial(t *testing.T) {
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, _ := ca.issue(t, host, nil)
	source := addBYOCSource(t, r, inst, cfg, cert, data)
	if _, err := r.reconcileBYOCTLSSecret(context.Background(), remote, inst, cfg, "target-a"); err != nil {
		t.Fatal(err)
	}
	source.Annotations = map[string]string{byocTLSRevokedAnnotation: byocTLSRevokedValue}
	if err := r.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileBYOCTLSSecret(context.Background(), remote, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatal("revoked source fell back to cached key")
	}
}
func TestBYOCTLSNoPendingCertificateDelivery(t *testing.T) {
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, _ := ca.issue(t, host, nil)
	addBYOCSource(t, r, inst, cfg, cert, data)
	cert.Object["status"] = map[string]interface{}{"state": byocCertificateReadyState, "observedGeneration": int64(0)}
	if err := r.Update(context.Background(), cert); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileBYOCTLSSecret(context.Background(), remote, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("unobserved Certificate accepted: %v", err)
	}
	var got corev1.Secret
	if err := remote.Get(context.Background(), client.ObjectKey{Namespace: byocNamespace, Name: cert.GetName()}, &got); !apierrors.IsNotFound(err) {
		t.Fatal("unobserved cert was delivered")
	}
}
func TestBYOCTLSDNSRejectsUntrustedAddresses(t *testing.T) {
	for _, bad := range []string{"", "127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "host.example.test", "https://194.11.242.27", "224.0.0.1", "0.1.2.3"} {
		if validBYOCDNSTarget(bad) {
			t.Fatalf("accepted private/reserved/untrusted target %q", bad)
		}
	}
	if !validBYOCDNSTarget("194.11.242.27") {
		t.Fatal("rejected public pilot IP")
	}
	r, _, inst, cfg, cert, _ := byocTLSFixture(t)
	if err := r.reconcileBYOCDNS(context.Background(), inst, cfg, "194.11.242.27", "target-a"); err != nil {
		t.Fatal(err)
	}
	obj := byocTLSObject(byocDNSGVK, cfg.Namespace, cert.GetName())
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatal(err)
	}
	obj.SetLabels(map[string]string{byocTLSOwnerLabel: "foreign"})
	if err := r.Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCDNS(context.Background(), inst, cfg, "194.11.242.28", "target-a"); err == nil {
		t.Fatal("foreign DNS entry adopted")
	}
}
func TestBYOCTLSCertificateDoesNotAdoptForeignSecret(t *testing.T) {
	ctx := context.Background()
	r, _, inst, cfg, cert, _ := byocTLSFixture(t)
	if err := r.Delete(ctx, cert); err != nil {
		t.Fatal(err)
	}
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cfg.Namespace, Name: cert.GetName()}, Data: map[string][]byte{"preserve": []byte("foreign")}}
	if err := r.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err == nil {
		t.Fatal("certificate request would overwrite foreign Secret")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); !apierrors.IsNotFound(err) {
		t.Fatal("Certificate created before source ownership established")
	}
}
func TestBYOCTLSCleanupOrdersDNSAndIssuanceBeforeSecrets(t *testing.T) {
	ctx := context.Background()
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, _ := ca.issue(t, host, nil)
	addBYOCSource(t, r, inst, cfg, cert, data)
	if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCDNS(ctx, inst, cfg, "194.11.242.27", "target-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("DNS deletion did not wait: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); err != nil {
		t.Fatal("issuance removed before DNS deletion confirmation")
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("Certificate deletion did not wait: %v", err)
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("managed Secret deletion did not wait: %v", err)
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{cert.GetName(), cert.GetName() + "-last-valid"} {
		var secret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, &secret); !apierrors.IsNotFound(err) {
			t.Fatalf("owned TLS material retained: %s", name)
		}
	}
	if err := cleanupBYOCRemoteTLS(ctx, remote, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("remote TLS deletion did not wait: %v", err)
	}
	if err := cleanupBYOCRemoteTLS(ctx, remote, inst, cfg, "target-a"); err != nil {
		t.Fatal("cleanup not idempotent")
	}
}

func TestBYOCTLSManagedCertificateCannotRelabelForeignOutput(t *testing.T) {
	ctx := context.Background()
	r, _, inst, cfg, cert, _ := byocTLSFixture(t)
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cfg.Namespace, Name: cert.GetName()}, Type: corev1.SecretTypeTLS}
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err == nil {
		t.Fatal("foreign output accepted despite an existing managed Certificate")
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); err == nil {
		t.Fatal("cleanup accepted a foreign output")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); err != nil {
		t.Fatal("Certificate removed before source identity could be established")
	}
}

func TestBYOCTLSOtherTargetCannotDeliverOrClean(t *testing.T) {
	ctx := context.Background()
	r, remote, inst, cfg, cert, ca := byocTLSFixture(t)
	_, host, _ := byocTLSIdentity(inst, cfg, "target-a")
	data, _ := ca.issue(t, host, nil)
	addBYOCSource(t, r, inst, cfg, cert, data)
	if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, cfg, "target-b"); err == nil {
		t.Fatal("target replacement reused old target certificate")
	}
	if err := r.reconcileBYOCDNS(ctx, inst, cfg, "194.11.242.27", "target-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCDNS(ctx, inst, cfg, "194.11.242.28", "target-b"); err != nil {
		t.Fatal(err)
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-b"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatal("target B DNS deletion did not wait")
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-b"); err != nil {
		t.Fatal(err)
	}
	activeDNS := byocTLSObject(byocDNSGVK, cfg.Namespace, cert.GetName())
	if err := r.Get(ctx, client.ObjectKeyFromObject(activeDNS), activeDNS); err != nil {
		t.Fatal("cleanup for a different target removed active DNS")
	}
	var existing corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: cert.GetName()}, &existing); err != nil {
		t.Fatal("cleanup for a different target removed active TLS material")
	}
	if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: cert.GetName()}, &existing); err != nil {
		t.Fatal("different target delivery modified active remote material")
	}
}

func TestBYOCTLSCleanupNeedsStoredCoordinatesOnly(t *testing.T) {
	ctx := context.Background()
	r, _, inst, cfg, cert, _ := byocTLSFixture(t)
	// Disabling issuance must not strand an existing instance finalizer. Cleanup
	// only needs the persisted namespace/domain and target identity, not an issuer.
	cfg.IssuerName = ""
	cfg.IssuerNamespace = ""
	cfg.DNSClass = ""
	if err := r.reconcileBYOCManagedCertificate(ctx, inst, cfg, "target-a"); err == nil {
		t.Fatal("missing issuer was accepted for issuance")
	}
	if err := r.reconcileBYOCDNS(ctx, inst, cfg, "194.11.242.27", "target-a"); err == nil {
		t.Fatal("missing DNS class was accepted for publishing")
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); !errors.Is(err, ErrBYOCTLSNotReady) {
		t.Fatalf("cleanup required unused issuer configuration: %v", err)
	}
	if err := r.cleanupBYOCManagedTLS(ctx, inst, cfg, "target-a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cert), cert); !apierrors.IsNotFound(err) {
		t.Fatal("Certificate survived confirmed cleanup")
	}
}
