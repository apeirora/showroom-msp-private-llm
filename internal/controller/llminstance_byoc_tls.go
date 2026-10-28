/*
Copyright 2026.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

const (
	byocTLSOwnerLabel         = "llm.privatellms.msp/tls-owner-uid"
	byocTLSCertificateLabel   = "llm.privatellms.msp/tls-certificate-uid"
	byocTLSTargetLabel        = "llm.privatellms.msp/tls-target"
	byocTLSRenewalMargin      = 2 * time.Minute
	byocCertificateReadyState = "Ready"
	byocTLSRevokedAnnotation  = "cert.gardener.cloud/revoked"
	byocTLSRevokedValue       = "true"
)

// BYOCTLSConfig is operator configuration, never populated from a tenant object.
// Namespace must be isolated from tenant and related-Secret synchronization.
type BYOCTLSConfig struct {
	Domain          string
	Namespace       string
	IssuerName      string
	IssuerNamespace string
	DNSClass        string
	// roots is only set by in-package tests. Production uses the OS trust store.
	roots *x509.CertPool
}

// ErrBYOCTLSNotReady requires retry without publishing an endpoint.
var ErrBYOCTLSNotReady = errors.New("BYOC TLS is not ready")

var byocCertificateGVK = schema.GroupVersionKind{Group: "cert.gardener.cloud", Version: "v1alpha1", Kind: "Certificate"}
var byocDNSGVK = schema.GroupVersionKind{Group: "dns.gardener.cloud", Version: "v1alpha1", Kind: "DNSEntry"}

func byocTLSIdentity(inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) (string, string, error) {
	if inst.UID == "" {
		return "", "", fmt.Errorf("BYOC TLS requires an instance UID")
	}
	if len(validation.IsDNS1123Subdomain(cfg.Domain)) != 0 || !strings.Contains(cfg.Domain, ".") {
		return "", "", fmt.Errorf("BYOC TLS requires an operator configured DNS domain")
	}
	if len(validation.IsDNS1123Label(cfg.Namespace)) != 0 || cfg.Namespace == inst.Namespace {
		return "", "", fmt.Errorf("BYOC TLS requires a separate operator credential namespace")
	}
	if targetID == "" {
		return "", "", fmt.Errorf("BYOC TLS requires a verified target identity")
	}
	sum := sha256.Sum256([]byte(string(inst.UID) + "\x00" + targetID))
	name := "byoc-" + hex.EncodeToString(sum[:])[:32]
	hostname := name + "." + cfg.Domain
	if len(validation.IsDNS1123Subdomain(hostname)) != 0 {
		return "", "", fmt.Errorf("BYOC TLS hostname is invalid")
	}
	return name, hostname, nil
}

func validateBYOCTLSIssuer(cfg BYOCTLSConfig) error {
	if cfg.IssuerName == "" || len(validation.IsDNS1123Subdomain(cfg.IssuerName)) != 0 || len(validation.IsDNS1123Label(cfg.IssuerNamespace)) != 0 {
		return fmt.Errorf("BYOC TLS issuer namespace and name are required")
	}
	return nil
}

func byocTLSObject(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func requireBYOCTLSOwner(obj client.Object, inst *llmv1alpha1.LLMInstance) error {
	if obj.GetLabels()[byocTLSOwnerLabel] != string(inst.UID) || inst.UID == "" {
		return fmt.Errorf("refusing foreign BYOC TLS object %s/%s", obj.GetNamespace(), obj.GetName())
	}
	// MSP objects live in a different namespace; remote objects in another cluster.
	// Neither can safely carry an LLMInstance ownerReference.
	if len(obj.GetOwnerReferences()) != 0 {
		return fmt.Errorf("unexpected ownerReference on BYOC TLS object %s/%s", obj.GetNamespace(), obj.GetName())
	}
	return nil
}

func byocTLSTarget(targetID string) (string, error) {
	if targetID == "" {
		return "", fmt.Errorf("BYOC TLS requires a verified target identity")
	}
	sum := sha256.Sum256([]byte(targetID))
	return hex.EncodeToString(sum[:])[:32], nil
}

func (r *LLMInstanceReconciler) reconcileBYOCManagedCertificate(ctx context.Context, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) error {
	if err := validateBYOCTLSIssuer(cfg); err != nil {
		return err
	}
	name, hostname, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return err
	}
	cert := byocTLSObject(byocCertificateGVK, cfg.Namespace, name)
	err = r.Get(ctx, client.ObjectKeyFromObject(cert), cert)
	if apierrors.IsNotFound(err) {
		// cert-management can overwrite an existing named Secret. Refuse to request
		// issuance when that name is occupied, even if its certificate looks valid.
		for _, secretName := range []string{name, name + "-last-valid"} {
			var secret corev1.Secret
			if err = r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: secretName}, &secret); err == nil {
				return fmt.Errorf("refusing pre-existing BYOC TLS Secret %s/%s", cfg.Namespace, secretName)
			} else if !apierrors.IsNotFound(err) {
				return err
			}
		}
		cert.SetLabels(map[string]string{byocTLSOwnerLabel: string(inst.UID)})
		cert.Object["spec"] = byocCertificateSpec(name, hostname, "", cfg, inst)
		if err = r.Create(ctx, cert); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err = requireBYOCTLSOwner(cert, inst); err != nil {
		return err
	}
	if cert.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%w: Certificate is deleting", ErrBYOCTLSNotReady)
	}
	if cert.GetUID() == "" {
		return fmt.Errorf("%w: Certificate has no UID", ErrBYOCTLSNotReady)
	}
	// Guard the named output before changing the Certificate. cert-management
	// propagates secretLabels and could otherwise relabel a foreign Secret.
	var source corev1.Secret
	if err = r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, &source); err == nil {
		if err = requireBYOCSourceSecret(&source, cert, inst); err != nil {
			// The only permitted transition is the first issuance racing the immediate
			// addition of the newly assigned Certificate UID to spec.secretLabels.
			oldUID, _, _ := unstructured.NestedString(cert.Object, "spec", "secretLabels", byocTLSCertificateLabel)
			firstIssuance := oldUID == "" && source.Labels[byocTLSCertificateLabel] == "" && source.Labels[byocTLSOwnerLabel] == string(inst.UID) && source.Type == corev1.SecretTypeTLS
			for _, owner := range source.OwnerReferences {
				if owner.APIVersion != byocCertificateGVK.GroupVersion().String() || owner.Kind != byocCertificateGVK.Kind || owner.Name != name || owner.UID != cert.GetUID() {
					firstIssuance = false
				}
			}
			if !firstIssuance {
				return err
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	desired := byocCertificateSpec(name, hostname, string(cert.GetUID()), cfg, inst)
	if reflect.DeepEqual(cert.Object["spec"], desired) {
		return nil
	}
	cert.Object["spec"] = desired
	return r.Update(ctx, cert)
}

func byocCertificateSpec(name, hostname, certificateUID string, cfg BYOCTLSConfig, inst *llmv1alpha1.LLMInstance) map[string]interface{} {
	labels := map[string]interface{}{byocTLSOwnerLabel: string(inst.UID)}
	if certificateUID != "" {
		labels[byocTLSCertificateLabel] = certificateUID
	}
	return map[string]interface{}{
		// Avoid the 64-character CommonName limit; SAN alone is supported by Gardener.
		"dnsNames":     []interface{}{hostname},
		"secretRef":    map[string]interface{}{"name": name, "namespace": cfg.Namespace},
		"issuerRef":    map[string]interface{}{"name": cfg.IssuerName, "namespace": cfg.IssuerNamespace},
		"secretLabels": labels,
	}
}

func validBYOCDNSTarget(address string) bool {
	ip := net.ParseIP(address)
	// The current implementation supports public IP LoadBalancers only. A CNAME
	// supplied by a tenant may resolve to an internal service after validation.
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	// IsGlobalUnicast includes special-use ranges unsuitable for a public API.
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(cidr).Contains(addr) {
			return false
		}
	}
	return true
}

func (r *LLMInstanceReconciler) reconcileBYOCDNS(ctx context.Context, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, address, targetID string) error {
	if cfg.DNSClass == "" {
		return fmt.Errorf("BYOC TLS DNS class is required")
	}
	name, hostname, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return err
	}
	if !validBYOCDNSTarget(address) {
		return fmt.Errorf("BYOC TLS requires a public unicast LoadBalancer IP address")
	}
	obj := byocTLSObject(byocDNSGVK, cfg.Namespace, name)
	desired := map[string]interface{}{"dnsName": hostname, "targets": []interface{}{address}, "ttl": int64(60)}
	if err = r.Get(ctx, client.ObjectKeyFromObject(obj), obj); apierrors.IsNotFound(err) {
		obj.SetLabels(map[string]string{byocTLSOwnerLabel: string(inst.UID)})
		obj.SetAnnotations(map[string]string{"dns.gardener.cloud/class": cfg.DNSClass})
		obj.Object["spec"] = desired
		return r.Create(ctx, obj)
	} else if err != nil {
		return err
	}
	if err = requireBYOCTLSOwner(obj, inst); err != nil {
		return err
	}
	if obj.GetDeletionTimestamp() != nil {
		return fmt.Errorf("%w: DNSEntry is deleting", ErrBYOCTLSNotReady)
	}
	annotations := obj.GetAnnotations()
	if reflect.DeepEqual(obj.Object["spec"], desired) && annotations["dns.gardener.cloud/class"] == cfg.DNSClass {
		return nil
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["dns.gardener.cloud/class"] = cfg.DNSClass
	obj.SetAnnotations(annotations)
	obj.Object["spec"] = desired
	return r.Update(ctx, obj)
}

func requireBYOCSourceSecret(secret *corev1.Secret, cert *unstructured.Unstructured, inst *llmv1alpha1.LLMInstance) error {
	if cert.GetUID() == "" || secret.Labels[byocTLSOwnerLabel] != string(inst.UID) || secret.Labels[byocTLSCertificateLabel] != string(cert.GetUID()) {
		return fmt.Errorf("source TLS Secret is not bound to the instance Certificate")
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion != byocCertificateGVK.GroupVersion().String() || owner.Kind != byocCertificateGVK.Kind || owner.Name != cert.GetName() || owner.UID != cert.GetUID() {
			return fmt.Errorf("source TLS Secret has a foreign ownerReference")
		}
	}
	if secret.Type != corev1.SecretTypeTLS {
		return fmt.Errorf("source TLS Secret has an unexpected type")
	}
	return nil
}

// validateBYOCTLSMaterial returns the public leaf fingerprint used both for
// rollout and end-to-end readiness. No key material appears in errors or status.
func validateBYOCTLSMaterial(data map[string][]byte, hostname string, now time.Time, roots *x509.CertPool) (string, error) {
	pair, err := tls.X509KeyPair(data[corev1.TLSCertKey], data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return "", fmt.Errorf("TLS certificate and key are invalid or do not match")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("TLS leaf certificate is invalid")
	}
	if leaf.IsCA || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != hostname || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return "", fmt.Errorf("TLS certificate must contain only the instance DNS SAN")
	}
	if !now.Before(leaf.NotAfter.Add(-byocTLSRenewalMargin)) || now.Before(leaf.NotBefore) {
		return "", fmt.Errorf("TLS certificate is outside its usable validity period")
	}
	serverAuth := false
	for _, eku := range leaf.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		return "", fmt.Errorf("TLS certificate lacks serverAuth usage")
	}
	switch key := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return "", fmt.Errorf("TLS RSA key is too small")
		}
	case *ecdsa.PublicKey:
		if key.Curve.Params().BitSize < 256 {
			return "", fmt.Errorf("TLS ECDSA key is too small")
		}
	default:
		return "", fmt.Errorf("TLS certificate has an unsupported public key type")
	}
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		intermediate, err := x509.ParseCertificate(raw)
		if err != nil {
			return "", fmt.Errorf("TLS certificate chain is invalid")
		}
		intermediates.AddCert(intermediate)
	}
	if _, err = leaf.Verify(x509.VerifyOptions{DNSName: hostname, Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return "", fmt.Errorf("TLS certificate chain or hostname verification failed")
	}
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func requireBYOCTargetSecret(secret *corev1.Secret, inst *llmv1alpha1.LLMInstance, target string) error {
	if err := requireBYOCTLSOwner(secret, inst); err != nil {
		return err
	}
	if secret.Labels[byocTLSTargetLabel] != target || secret.Type != corev1.SecretTypeTLS {
		return fmt.Errorf("TLS Secret is not bound to the verified target")
	}
	return nil
}

func copyBYOCTLSData(data map[string][]byte) map[string][]byte {
	return map[string][]byte{corev1.TLSCertKey: append([]byte(nil), data[corev1.TLSCertKey]...), corev1.TLSPrivateKeyKey: append([]byte(nil), data[corev1.TLSPrivateKeyKey]...)}
}

func writeBYOCTLSSecret(ctx context.Context, c client.Client, secret *corev1.Secret, inst *llmv1alpha1.LLMInstance, target string, data map[string][]byte) error {
	err := c.Get(ctx, client.ObjectKeyFromObject(secret), secret)
	if apierrors.IsNotFound(err) {
		secret.Labels = map[string]string{byocTLSOwnerLabel: string(inst.UID), byocTLSTargetLabel: target}
		secret.Type = corev1.SecretTypeTLS
		secret.Data = copyBYOCTLSData(data)
		return c.Create(ctx, secret)
	} else if err != nil {
		return err
	}
	if err = requireBYOCTargetSecret(secret, inst, target); err != nil {
		return err
	}
	if secret.DeletionTimestamp != nil {
		return fmt.Errorf("%w: TLS Secret is deleting", ErrBYOCTLSNotReady)
	}
	desired := copyBYOCTLSData(data)
	if reflect.DeepEqual(secret.Data, desired) {
		return nil
	}
	secret.Data = desired
	return c.Update(ctx, secret)
}

// reconcileBYOCTLSSecret copies validated material through a verified remote
// client. The caller must reject unsafe kubeconfigs before constructing remote.
// A protected MSP cache retains a valid serving pair during issuance failures;
// key material is never recovered from the tenant-controlled remote cluster.
func (r *LLMInstanceReconciler) reconcileBYOCTLSSecret(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) (string, error) {
	if err := validateBYOCTLSIssuer(cfg); err != nil {
		return "", err
	}
	name, hostname, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return "", err
	}
	target, err := byocTLSTarget(targetID)
	if err != nil {
		return "", err
	}
	cert := byocTLSObject(byocCertificateGVK, cfg.Namespace, name)
	if err = r.Get(ctx, client.ObjectKeyFromObject(cert), cert); err != nil {
		return "", err
	}
	if err = requireBYOCTLSOwner(cert, inst); err != nil {
		return "", err
	}
	if cert.GetDeletionTimestamp() != nil {
		return "", fmt.Errorf("%w: Certificate is deleting", ErrBYOCTLSNotReady)
	}
	// The desired spec and identity must already have converged. Never deliver a
	// certificate through an operator config change before explicit reconciliation.
	if !reflect.DeepEqual(cert.Object["spec"], byocCertificateSpec(name, hostname, string(cert.GetUID()), cfg, inst)) {
		return "", fmt.Errorf("%w: Certificate specification has not converged", ErrBYOCTLSNotReady)
	}
	state, _, _ := unstructured.NestedString(cert.Object, "status", "state")
	if state == "Revoked" || cert.GetAnnotations()[byocTLSRevokedAnnotation] == byocTLSRevokedValue {
		return "", fmt.Errorf("%w: certificate has been revoked", ErrBYOCTLSNotReady)
	}
	now := time.Now()
	var source corev1.Secret
	sourceErr := r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, &source)
	var data map[string][]byte
	var revision string
	if sourceErr == nil {
		if err = requireBYOCSourceSecret(&source, cert, inst); err != nil {
			return "", err
		}
		if source.Annotations[byocTLSRevokedAnnotation] == byocTLSRevokedValue {
			return "", fmt.Errorf("%w: certificate has been revoked", ErrBYOCTLSNotReady)
		}
		observed, _, _ := unstructured.NestedInt64(cert.Object, "status", "observedGeneration")
		if state == byocCertificateReadyState && observed == cert.GetGeneration() {
			revision, sourceErr = validateBYOCTLSMaterial(source.Data, hostname, now, cfg.roots)
			if sourceErr == nil {
				data = source.Data
			}
		}
	} else if !apierrors.IsNotFound(sourceErr) {
		return "", sourceErr
	}
	cache := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cfg.Namespace, Name: name + "-last-valid"}}
	if data != nil {
		if err = writeBYOCTLSSecret(ctx, r.Client, cache, inst, target, data); err != nil {
			return "", err
		}
	} else {
		if err = r.Get(ctx, client.ObjectKeyFromObject(cache), cache); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("%w: no valid issued certificate", ErrBYOCTLSNotReady)
			}
			return "", err
		}
		if err = requireBYOCTargetSecret(cache, inst, target); err != nil {
			return "", err
		}
		if revision, err = validateBYOCTLSMaterial(cache.Data, hostname, now, cfg.roots); err != nil {
			return "", fmt.Errorf("%w: last serving certificate is no longer valid", ErrBYOCTLSNotReady)
		}
		data = cache.Data
	}
	destination := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: name}}
	if err = writeBYOCTLSSecret(ctx, remote, destination, inst, target, data); err != nil {
		return "", err
	}
	return revision, nil
}

// cleanupBYOCDNS must finish before releasing the remote LoadBalancer. Waiting
// for the DNSEntry finalizer prevents publishing a dangling address while the
// controller still has a delete operation pending at the DNS provider.
func (r *LLMInstanceReconciler) cleanupBYOCDNS(ctx context.Context, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) error {
	name, _, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return err
	}
	obj := byocTLSObject(byocDNSGVK, cfg.Namespace, name)
	if err = r.Get(ctx, client.ObjectKeyFromObject(obj), obj); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err = requireBYOCTLSOwner(obj, inst); err != nil {
		return err
	}
	if obj.GetDeletionTimestamp() == nil {
		uid, rv := obj.GetUID(), obj.GetResourceVersion()
		if err = r.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return fmt.Errorf("%w: waiting for DNSEntry removal", ErrBYOCTLSNotReady)
}

func cleanupBYOCRemoteTLS(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) error {
	name, _, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return err
	}
	target, err := byocTLSTarget(targetID)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{}
	if err = remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, secret); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err = requireBYOCTargetSecret(secret, inst, target); err != nil {
		return err
	}
	if secret.DeletionTimestamp == nil {
		uid, rv := secret.UID, secret.ResourceVersion
		if err = client.IgnoreNotFound(remote.Delete(ctx, secret, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: waiting for remote TLS Secret removal", ErrBYOCTLSNotReady)
}

func (r *LLMInstanceReconciler) cleanupBYOCManagedTLS(ctx context.Context, inst *llmv1alpha1.LLMInstance, cfg BYOCTLSConfig, targetID string) error {
	if err := r.cleanupBYOCDNS(ctx, inst, cfg, targetID); err != nil {
		return err
	}
	name, _, err := byocTLSIdentity(inst, cfg, targetID)
	if err != nil {
		return err
	}
	target, err := byocTLSTarget(targetID)
	if err != nil {
		return err
	}
	cert := byocTLSObject(byocCertificateGVK, cfg.Namespace, name)
	err = r.Get(ctx, client.ObjectKeyFromObject(cert), cert)
	if err == nil {
		if err = requireBYOCTLSOwner(cert, inst); err != nil {
			return err
		}
		if cert.GetDeletionTimestamp() == nil {
			// Finish the initial UID-label handshake before stopping issuance. It lets
			// a deletion retry identify and remove the source after the Certificate is
			// gone, including when deletion races the first issuance.
			var source corev1.Secret
			if sourceErr := r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: name}, &source); sourceErr == nil {
				if sourceErr = requireBYOCSourceSecret(&source, cert, inst); sourceErr != nil {
					if err = r.reconcileBYOCManagedCertificate(ctx, inst, cfg, targetID); err != nil {
						return err
					}
					return fmt.Errorf("%w: waiting for source Certificate ownership", ErrBYOCTLSNotReady)
				}
			} else if !apierrors.IsNotFound(sourceErr) {
				return sourceErr
			}
			uid, rv := cert.GetUID(), cert.GetResourceVersion()
			if err = client.IgnoreNotFound(r.Delete(ctx, cert, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})); err != nil {
				return err
			}
		}
		// Wait until issuance has stopped before removing its output. Otherwise an
		// in-flight cert-management reconcile could recreate key material afterward.
		return fmt.Errorf("%w: waiting for Certificate removal", ErrBYOCTLSNotReady)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	secretsPending := false
	for _, secretName := range []string{name, name + "-last-valid"} {
		var secret corev1.Secret
		if err = r.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: secretName}, &secret); apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return err
		}
		if secretName == name {
			// The Certificate has now gone. Its UID label was requested in secretLabels
			// and is writable only by operators in this protected namespace. Validate
			// any remaining ownerRefs against it, rather than accepting a foreign owner.
			certificateUID := secret.Labels[byocTLSCertificateLabel]
			if secret.Labels[byocTLSOwnerLabel] != string(inst.UID) || certificateUID == "" || secret.Type != corev1.SecretTypeTLS {
				return fmt.Errorf("cannot verify orphaned source TLS Secret ownership")
			}
			for _, owner := range secret.OwnerReferences {
				if owner.APIVersion != byocCertificateGVK.GroupVersion().String() || owner.Kind != byocCertificateGVK.Kind || owner.Name != name || string(owner.UID) != certificateUID {
					return fmt.Errorf("orphaned source TLS Secret has a foreign ownerReference")
				}
			}
		} else if err = requireBYOCTargetSecret(&secret, inst, target); err != nil {
			return err
		}
		secretsPending = true
		if secret.DeletionTimestamp == nil {
			uid, rv := secret.UID, secret.ResourceVersion
			if err = client.IgnoreNotFound(r.Delete(ctx, &secret, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})); err != nil {
				return err
			}
		}
	}
	if secretsPending {
		return fmt.Errorf("%w: waiting for managed TLS Secret removal", ErrBYOCTLSNotReady)
	}
	return nil
}
