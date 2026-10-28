package controller

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func transportKubeconfig(server, clusterExtra, authExtra string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: byoc
clusters:
- name: byoc
  cluster:
    server: %s
%s
contexts:
- name: byoc
  context: {cluster: byoc, user: byoc}
users:
- name: byoc
  user:
    token: fixture-token
%s
`, server, clusterExtra, authExtra))
}

func TestBYOCRejectsUnsafeKubeconfigs(t *testing.T) {
	cases := []struct{ name, server, cluster, auth string }{
		{"http", "http://api.example.com", "", ""},
		{"userinfo", "https://user:pass@api.example.com", "", ""},
		{"query", "https://api.example.com?token=x", "", ""},
		{"insecure", "https://api.example.com", "    insecure-skip-tls-verify: true", ""},
		{"cafile", "https://api.example.com", "    certificate-authority: /etc/secret", ""},
		{"proxy", "https://api.example.com", "    proxy-url: http://proxy.invalid", ""},
		{"exec", "https://api.example.com", "", "    exec: {apiVersion: client.authentication.k8s.io/v1, command: unsafe}"},
		{"authprovider", "https://api.example.com", "", "    auth-provider: {name: gcp}"},
		{"tokenfile", "https://api.example.com", "", "    tokenFile: /etc/secret"},
		{"keyfile", "https://api.example.com", "", "    client-key: /etc/secret"},
		{"certfile", "https://api.example.com", "", "    client-certificate: /etc/secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := verifiedBYOCRESTConfig(transportKubeconfig(tc.server, tc.cluster, tc.auth)); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
	cfg, err := verifiedBYOCRESTConfig(transportKubeconfig("https://api.example.com", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := cfg.Proxy(&http.Request{})
	if err != nil || proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	first, _ := byocTargetIdentity(transportKubeconfig("https://api.example.com", "", ""))
	changed, _ := byocTargetIdentity(transportKubeconfig("https://other.example.com", "", ""))
	rotated, _ := byocTargetIdentity([]byte(strings.ReplaceAll(string(transportKubeconfig("https://api.example.com", "", "")), "fixture-token", "rotated-token")))
	if first == changed || first != rotated {
		t.Fatal("target identity confuses cluster and credential rotation")
	}
}

func TestBYOCAPIDeliveryRejectsRedirects(t *testing.T) {
	var received atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
	}))
	defer dest.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", dest.URL)
				w.WriteHeader(code)
			}))
			defer source.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.Certificate().Raw})
			cfg, err := verifiedBYOCRESTConfig(transportKubeconfig(source.URL, "    certificate-authority-data: "+base64.StdEncoding.EncodeToString(ca), ""))
			if err != nil {
				t.Fatal(err)
			}
			c, err := byocAPIHTTPClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodPost, source.URL+"/api/v1/namespaces/private-llm/secrets", strings.NewReader("fixture-certificate-payload"))
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != code || received.Load() != 0 {
				t.Fatal("credential delivery followed redirect")
			}
		})
	}
}

func TestBYOCHealthRequiresDeliveredCertificateAndNoRedirects(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://unsafe.invalid")
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	sum := sha256.Sum256(server.Certificate().Raw)
	fingerprint := hex.EncodeToString(sum[:])
	r := &LLMInstanceReconciler{BYOCTLS: BYOCTLSConfig{roots: roots}}
	if err := r.runBYOCHealthCheck(context.Background(), server.URL, fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := r.runBYOCHealthCheck(context.Background(), server.URL, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong delivered certificate accepted")
	}
	status.Store(302)
	if err := r.runBYOCHealthCheck(context.Background(), server.URL, fingerprint); err == nil {
		t.Fatal("redirect accepted")
	}
	r.BYOCTLS.roots = x509.NewCertPool()
	status.Store(200)
	if err := r.runBYOCHealthCheck(context.Background(), server.URL, fingerprint); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	if err := r.runBYOCHealthCheck(context.Background(), "http://localhost", fingerprint); err == nil {
		t.Fatal("HTTP probe accepted")
	}
}

func TestBYOCWorkloadClosesPlaintextPorts(t *testing.T) {
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "fixture", Namespace: "tenant", UID: types.UID("fixture-uid")}}
	deploy := buildBYOCDeployment(inst, "fixture-slug", 1, modelForInstance(inst), "keys-rv", "cert", "cert-fingerprint")
	p := deploy.Spec.Template.Spec
	if len(p.Containers) != 2 || p.AutomountServiceAccountToken == nil || *p.AutomountServiceAccountToken || deploy.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("unsafe pod shape")
	}
	model, proxy := p.Containers[0], p.Containers[1]
	if strings.Contains(strings.Join(model.Args, " "), "0.0.0.0") || !strings.Contains(strings.Join(model.Args, " "), "--host 127.0.0.1") || len(model.Ports) != 0 || model.ReadinessProbe.Exec == nil {
		t.Fatal("model remains publicly bound")
	}
	if proxy.Image != byocProxyImage || !strings.Contains(proxy.Image, "@sha256:") || proxy.Ports[0].ContainerPort != 8443 {
		t.Fatal("listener not pinned or wrong port")
	}
	for _, mount := range proxy.VolumeMounts {
		if mount.SubPath != "" || !mount.ReadOnly {
			t.Fatal("unsafe TLS mount")
		}
	}
	svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 443, NodePort: 30443, TargetPort: intstr.FromInt(8000)}, {Name: "model", Port: 8000}, {Name: "web", Port: 80}}}}
	if !ensureBYOCServicePort(svc) || len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].TargetPort.IntValue() != 8443 || svc.Spec.Ports[0].NodePort != 30443 {
		t.Fatal("old plaintext ports survived")
	}
	if ensureBYOCServicePort(svc) {
		t.Fatal("service is not stable")
	}
}

func TestBYOCOwnershipAndKeySelection(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = llmv1alpha1.AddToScheme(scheme)
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "fixture", Namespace: "tenant", UID: "instance"}, Status: llmv1alpha1.LLMInstanceStatus{ActiveBYOCTargetID: "target"}}
	tr := &llmv1alpha1.APITokenRequest{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "tenant", UID: "token-uid"}, Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: inst.Name}, Status: llmv1alpha1.APITokenRequestStatus{SecretName: "token-token"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token-token", Namespace: "tenant", Annotations: map[string]string{credentialTargetAnnotation: inst.Status.ActiveBYOCTargetID, credentialInstanceAnnotation: string(inst.UID)}, Labels: map[string]string{instanceLabelKey: inst.Name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: llmv1alpha1.GroupVersion.String(), Kind: "APITokenRequest", Name: tr.Name, UID: tr.UID, Controller: ptrTo(true)}}}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{openAIAPIKeyKey: []byte("owned-token")}}
	spoof := secret.DeepCopy()
	spoof.Name = "spoof"
	spoof.OwnerReferences = nil
	spoof.Data[openAIAPIKeyKey] = []byte("forged-token")
	local := fake.NewClientBuilder().WithScheme(scheme).WithObjects(inst, tr, secret, spoof).Build()
	remote := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &LLMInstanceReconciler{Client: local, Scheme: scheme}
	keys, _, err := r.desiredAPIKeyFile(ctx, remote, inst, "slug")
	if err != nil || keys != "owned-token\n" {
		t.Fatalf("selected foreign token: %v", err)
	}
	for _, annotation := range []string{credentialTargetAnnotation, credentialInstanceAnnotation} {
		original := secret.Annotations[annotation]
		secret.Annotations[annotation] = "previous-owner"
		if err := local.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
		selected, bootstrap, err := r.desiredAPIKeyFile(ctx, remote, inst, "slug")
		if err != nil || !bootstrap || strings.Contains(selected, "owned-token") {
			t.Fatal("previous target or instance key still selected")
		}
		secret.Annotations[annotation] = original
		if err := local.Update(ctx, secret); err != nil {
			t.Fatal(err)
		}
	}
	foreign := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: byocBaseName("slug"), Namespace: byocNamespace}}
	if err := remote.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileBYOCService(ctx, remote, inst, "slug"); err == nil {
		t.Fatal("adopted foreign service")
	}
	if err := cleanupBYOCObjects(ctx, remote, inst, "slug"); err == nil {
		t.Fatal("deleted foreign service")
	}
	if err := remote.Get(ctx, client.ObjectKeyFromObject(foreign), &corev1.Service{}); err != nil {
		t.Fatal("foreign service removed")
	}
}
