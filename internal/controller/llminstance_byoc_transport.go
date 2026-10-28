package controller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	byocDefaultServiceAccount = "default"
	byocOwnerUIDAnnotation    = "llm.privatellms.msp/owner-uid"
	byocTLSRevisionAnnotation = "llm.privatellms.msp/tls-revision"
	byocProxyImage            = "docker.io/library/traefik:v3.7.16@sha256:1336e85aaed293bd63d005086e0dd013b04078b26aaeb1c18b26a2f928af7e65"
)

// Kubeconfigs are tenant supplied. Never execute auth plugins or load local
// credential files, and never send a certificate key over an insecure API path.
func verifiedBYOCRESTConfig(data []byte) (*rest.Config, error) {
	raw, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("invalid BYOC kubeconfig")
	}
	current, ok := raw.Contexts[raw.CurrentContext]
	if !ok || current == nil {
		return nil, fmt.Errorf("BYOC kubeconfig has no current context")
	}
	cluster, ok := raw.Clusters[current.Cluster]
	if !ok || cluster == nil {
		return nil, fmt.Errorf("BYOC kubeconfig has no selected cluster")
	}
	u, err := url.Parse(cluster.Server)
	if err != nil || u.Scheme != byocServicePortName || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cluster.InsecureSkipTLSVerify || cluster.CertificateAuthority != "" || cluster.ProxyURL != "" {
		return nil, fmt.Errorf("BYOC API requires verified HTTPS and inline CA data without a proxy")
	}
	auth := raw.AuthInfos[current.AuthInfo]
	if auth != nil && (auth.Exec != nil || auth.AuthProvider != nil || auth.TokenFile != "" || auth.ClientCertificate != "" || auth.ClientKey != "") {
		return nil, fmt.Errorf("BYOC kubeconfig requires inline credentials; local files and auth plugins are forbidden")
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return nil, fmt.Errorf("invalid BYOC kubeconfig credentials")
	}
	cfg.Timeout = 30 * time.Second
	cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	return cfg, nil
}

func byocTargetIdentity(data []byte) (string, error) {
	cfg, err := verifiedBYOCRESTConfig(data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cfg.Host + "\x00" + cfg.ServerName + "\x00" + string(cfg.CAData)))
	return hex.EncodeToString(sum[:]), nil
}

func byocOwnedAnnotations(inst *llmv1alpha1.LLMInstance) map[string]string {
	return map[string]string{byocOwnerUIDAnnotation: string(inst.UID)}
}

func checkBYOCObjectOwner(obj client.Object, inst *llmv1alpha1.LLMInstance) error {
	if inst.UID == "" || obj.GetAnnotations()[byocOwnerUIDAnnotation] != string(inst.UID) || len(obj.GetOwnerReferences()) != 0 {
		return fmt.Errorf("refusing unowned BYOC object %s/%s; legacy objects require verified migration", obj.GetNamespace(), obj.GetName())
	}
	return nil
}

func (r *LLMInstanceReconciler) reconcileBYOCProxyConfig(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug, hostname string) error {
	desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: byocBaseName(slug) + "-proxy", Namespace: byocNamespace, Annotations: byocOwnedAnnotations(inst)}, Data: map[string]string{"dynamic.yaml": byocProxyConfig(hostname)}}
	var existing corev1.ConfigMap
	err := remote.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		return remote.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if err := checkBYOCObjectOwner(&existing, inst); err != nil {
		return err
	}
	if reflect.DeepEqual(existing.Data, desired.Data) {
		return nil
	}
	existing.Data = desired.Data
	return remote.Update(ctx, &existing)
}

func byocProxyConfig(hostname string) string {
	// hostname is produced by byocTLSIdentity from the operator domain and UID.
	return fmt.Sprintf(`http:
  routers:
    model:
      entryPoints: [websecure]
      rule: "Host(%s)"
      service: model
      tls: {}
  services:
    model:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:8000
        responseForwarding:
          flushInterval: -1ms
tls:
  certificates:
    - certFile: /tls/tls.crt
      keyFile: /tls/tls.key
  options:
    default:
      minVersion: VersionTLS12
      sniStrict: true
`, "`"+hostname+"`")
}

func configureBYOCTLSDeployment(deploy *appsv1.Deployment, inst *llmv1alpha1.LLMInstance, slug, tlsSecret, tlsRevision string) {
	deploy.Annotations = byocOwnedAnnotations(inst)
	deploy.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	spec := &deploy.Spec.Template.Spec
	// No Kubernetes API credential is needed by the workload or listener.
	spec.AutomountServiceAccountToken = ptrTo(false)
	model := &spec.Containers[0]
	for i := range model.Args {
		if i > 0 && model.Args[i-1] == "--host" {
			model.Args[i] = "127.0.0.1"
		}
	}
	model.Ports = nil
	// The pinned model image includes curl for the model download. Exec probes run
	// inside the pod network namespace; kubelet HTTP probes cannot reach loopback.
	health := []string{"curl", "--fail", "--silent", "--max-time", "2", "http://127.0.0.1:8000/health"}
	model.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: health}}, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 6}
	model.LivenessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: health}}, InitialDelaySeconds: 20, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 6}
	spec.Containers = append(spec.Containers, corev1.Container{
		Name: "tls-proxy", Image: byocProxyImage,
		Args:            []string{"--entryPoints.websecure.address=:8443", "--providers.file.filename=/config/dynamic.yaml", "--providers.file.watch=true", "--global.checkNewVersion=false", "--global.sendAnonymousUsage=false"},
		Ports:           []corev1.ContainerPort{{Name: "https", ContainerPort: 8443}},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8443)}}, PeriodSeconds: 5},
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")}},
		SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptrTo(true), RunAsUser: ptrTo(int64(65532)), AllowPrivilegeEscalation: ptrTo(false), ReadOnlyRootFilesystem: ptrTo(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "tls", MountPath: "/tls", ReadOnly: true}, {Name: "proxy-config", MountPath: "/config", ReadOnly: true}},
	})
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: tlsSecret}}}, corev1.Volume{Name: "proxy-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: byocBaseName(slug) + "-proxy"}}}})
	// Recreate prevents a Ready old replica from masking a failed renewal; a valid
	// old certificate is retained in the Secret until valid replacement is present.
	deploy.Spec.Template.Annotations[byocTLSRevisionAnnotation] = tlsRevision
	configHash := sha256.Sum256([]byte(tlsSecret + "\x00" + byocProxyImage))
	deploy.Spec.Template.Annotations["llm.privatellms.msp/proxy-revision"] = hex.EncodeToString(configHash[:])
}

func safeBYOCSlug(inst *llmv1alpha1.LLMInstance, slug string) string {
	if inst.Status.ActiveBYOCSlug != "" {
		return inst.Status.ActiveBYOCSlug
	}
	return strings.TrimSpace(slug)
}

// Pin both the API trust identity and the cluster's immutable namespace UID.
func byocClusterIdentity(ctx context.Context, remote client.Client, apiID string) (string, error) {
	var ns corev1.Namespace
	if err := remote.Get(ctx, client.ObjectKey{Name: "kube-system"}, &ns); err != nil {
		return "", fmt.Errorf("cannot establish BYOC cluster identity: %w", err)
	}
	if ns.UID == "" {
		return "", fmt.Errorf("BYOC cluster namespace has no UID")
	}
	sum := sha256.Sum256([]byte(apiID + "\x00" + string(ns.UID)))
	return hex.EncodeToString(sum[:]), nil
}

// Fill the API defaults that are persisted on the pod template. This allows
// full convergence without repeatedly restarting an unchanged workload.
func corev1defaults(deploy *appsv1.Deployment) {
	p := &deploy.Spec.Template.Spec
	p.RestartPolicy = corev1.RestartPolicyAlways
	p.TerminationGracePeriodSeconds = ptrTo(int64(30))
	p.SchedulerName = corev1.DefaultSchedulerName
	p.ServiceAccountName = byocDefaultServiceAccount
	p.DeprecatedServiceAccount = byocDefaultServiceAccount
	p.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptrTo(int64(65532))}
	defaultContainer := func(c *corev1.Container) {
		c.ImagePullPolicy = corev1.PullIfNotPresent
		c.TerminationMessagePath = corev1.TerminationMessagePathDefault
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		for j := range c.Ports {
			c.Ports[j].Protocol = corev1.ProtocolTCP
		}
		for _, probe := range []*corev1.Probe{c.ReadinessProbe, c.LivenessProbe} {
			if probe != nil {
				if probe.SuccessThreshold == 0 {
					probe.SuccessThreshold = 1
				}
				if probe.FailureThreshold == 0 {
					probe.FailureThreshold = 3
				}
				if probe.TimeoutSeconds == 0 {
					probe.TimeoutSeconds = 1
				}
			}
		}
	}
	for i := range p.Containers {
		defaultContainer(&p.Containers[i])
	}
	for i := range p.InitContainers {
		defaultContainer(&p.InitContainers[i])
	}
	for i := range p.Volumes {
		if p.Volumes[i].Secret != nil {
			mode := int32(0440)
			p.Volumes[i].Secret.DefaultMode = &mode
		}
		if p.Volumes[i].ConfigMap != nil {
			mode := int32(0444)
			p.Volumes[i].ConfigMap.DefaultMode = &mode
		}
	}
}

func (r *LLMInstanceReconciler) runBYOCHealthCheck(ctx context.Context, targetURL, fingerprint string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if r.BYOCHealthChecker != nil {
		return r.BYOCHealthChecker(ctx, targetURL, fingerprint)
	}
	u, err := url.Parse(targetURL)
	if err != nil || u.Scheme != byocServicePortName || fingerprint == "" {
		return fmt.Errorf("BYOC health requires HTTPS and a delivered certificate fingerprint")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: r.BYOCTLS.roots}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.TLS == nil || len(resp.TLS.VerifiedChains) == 0 || len(resp.TLS.PeerCertificates) == 0 {
		return fmt.Errorf("BYOC TLS certificate was not verified")
	}
	actual := sha256.Sum256(resp.TLS.PeerCertificates[0].Raw)
	if hex.EncodeToString(actual[:]) != fingerprint {
		return fmt.Errorf("BYOC endpoint is not serving the delivered instance certificate")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("BYOC health returned status %d", resp.StatusCode)
	}
	return nil
}

// Secret delivery must not replay requests across redirects, including a
// downgrade or an API server redirect to another cluster with a valid cert.
func byocAPIHTTPClient(cfg *rest.Config) (*http.Client, error) {
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return httpClient, nil
}
