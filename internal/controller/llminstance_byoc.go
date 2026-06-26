/*
Copyright 2025.

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

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

// BYOC mode: the LLM workload runs on a user-supplied cluster reached via the
// kubeconfig Secret referenced by spec.clusterRef. Owner references do not
// work across clusters, so remote objects are named after the instance slug
// and removed explicitly by the finalizer.
const (
	byocNamespace            = "private-llm"
	byocKubeconfigKey        = "kubeconfig"
	byocAPIKeyFileKey        = "api-keys"
	byocAPIKeyMountPath      = "/keys"
	byocServicePortName      = "http"
	byocServicePort          = int32(443)
	byocTargetPort           = int32(8000)
	byocAPIKeysRevAnnotation = "llm.privatellms.msp/api-keys-revision"
	byocBootstrapAnnotation  = "llm.privatellms.msp/bootstrap-key"
	byocBootstrapTrue        = "true"
	instanceLabelKey         = "llm.privatellms.msp/instance"
)

// byocBaseName derives an RFC 1123/1035-safe object name from the slug.
// Slugs are base64url and may contain uppercase characters, which are not
// allowed in resource names, so the slug is hashed into lowercase hex.
func byocBaseName(slug string) string {
	sum := sha256.Sum256([]byte(slug))
	return "llm-" + hex.EncodeToString(sum[:])[:12]
}

func byocKeySecretName(slug string) string {
	return byocBaseName(slug) + "-keys"
}

// byocCredentialsName is the operator-owned local copy of the BYOC kubeconfig.
// The sync agent deletes the synced kubeconfig Secret before the LLMInstance
// itself and waits for it to disappear, so cleanup cannot depend on it (a
// finalizer on it deadlocks the agent). The cache is invisible to the agent
// and lives exactly as long as the instance.
func byocCredentialsName(slug string) string {
	return byocBaseName(slug) + "-credentials"
}

func byocAPIKeyFilePath() string {
	return byocAPIKeyMountPath + "/" + byocAPIKeyFileKey
}

func byocLabels(instanceName, slug string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":   "llama-cpp-server",
		instanceLabelKey:           instanceName,
		"llm.privatellms.msp/slug": slug,
	}
}

func (r *LLMInstanceReconciler) defaultRemoteClient(kubeconfig []byte) (client.Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: r.Scheme})
}

// byocKubeconfig fetches the BYOC kubeconfig, preferring the user-supplied
// Secret and falling back to the operator's cached copy (the sync agent
// removes the synced Secret before the instance during deletion).
func (r *LLMInstanceReconciler) byocKubeconfig(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) ([]byte, string, string, error) {
	secretName := inst.Spec.ClusterRef.KubeconfigSecretName
	var sec corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: secretName}, &sec)
	if err == nil {
		if kubeconfig := sec.Data[byocKubeconfigKey]; len(kubeconfig) > 0 {
			return kubeconfig, "", "", nil
		}
		return nil, "KubeconfigInvalid", fmt.Sprintf("kubeconfig Secret %q has no %q key", secretName, byocKubeconfigKey), nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, "", "", err
	}

	var cached corev1.Secret
	if slug != "" {
		if cerr := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: byocCredentialsName(slug)}, &cached); cerr == nil {
			if kubeconfig := cached.Data[byocKubeconfigKey]; len(kubeconfig) > 0 {
				return kubeconfig, "", "", nil
			}
		}
	}
	return nil, "KubeconfigSecretMissing", fmt.Sprintf("kubeconfig Secret %q not found", secretName), nil
}

// byocClient builds a client for the BYOC cluster. A nil client with a
// non-empty reason signals a user-fixable provisioning condition rather than
// a reconcile error.
func (r *LLMInstanceReconciler) byocClient(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) (client.Client, string, string, error) {
	kubeconfig, reason, message, err := r.byocKubeconfig(ctx, inst, slug)
	if err != nil || kubeconfig == nil {
		return nil, reason, message, err
	}
	builder := r.RemoteClientBuilder
	if builder == nil {
		builder = r.defaultRemoteClient
	}
	remote, err := builder(kubeconfig)
	if err != nil {
		return nil, "KubeconfigInvalid", fmt.Sprintf("cannot build client for BYOC cluster: %v", err), nil
	}
	return remote, "", "", nil
}

// ensureCachedCredentials keeps an operator-owned copy of the kubeconfig so
// that remote cleanup still has credentials after the sync agent removed the
// user-supplied Secret. Owned by the instance, so GC removes it afterwards.
func (r *LLMInstanceReconciler) ensureCachedCredentials(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	kubeconfig, _, _, err := r.byocKubeconfig(ctx, inst, slug)
	if err != nil || kubeconfig == nil {
		return err
	}

	name := byocCredentialsName(slug)
	var existing corev1.Secret
	err = r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: name}, &existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: inst.Namespace,
				Labels:    byocLabels(inst.Name, slug),
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{byocKubeconfigKey: kubeconfig},
		}
		if err := ctrl.SetControllerReference(inst, &secret, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, &secret)
	}
	if string(existing.Data[byocKubeconfigKey]) == string(kubeconfig) {
		return nil
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	existing.Data[byocKubeconfigKey] = kubeconfig
	return r.Update(ctx, &existing)
}

func (r *LLMInstanceReconciler) reconcileBYOC(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string, model modelSelection) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if err := r.ensureCachedCredentials(ctx, inst, slug); err != nil {
		return ctrl.Result{}, err
	}

	remote, reason, message, err := r.byocClient(ctx, inst, slug)
	if err != nil {
		return ctrl.Result{}, err
	}
	if remote == nil {
		if err := r.updateProvisioningStatus(ctx, inst, "", reason, message); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("BYOC cluster not usable yet", "reason", reason)
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, nil
	}

	if err := r.ensureBYOCNamespace(ctx, remote); err != nil {
		return ctrl.Result{}, err
	}

	keyFile, bootstrap, err := r.desiredAPIKeyFile(ctx, remote, inst, slug)
	if err != nil {
		return ctrl.Result{}, err
	}
	keysRevision, err := r.reconcileBYOCKeySecret(ctx, remote, inst, slug, keyFile, bootstrap)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileBYOCDeployment(ctx, remote, inst, slug, model, keysRevision); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileBYOCService(ctx, remote, inst, slug); err != nil {
		return ctrl.Result{}, err
	}

	ready, endpoint, reason, message, err := r.evaluateBYOCReadiness(ctx, remote, slug)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		if err := r.updateProvisioningStatus(ctx, inst, endpoint, reason, message); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("BYOC LLMInstance is still provisioning", "reason", reason)
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, nil
	}

	if err := r.updateReadyStatus(ctx, inst, endpoint); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("reconciled BYOC LLMInstance", "endpoint", endpoint)
	return ctrl.Result{}, nil
}

func (r *LLMInstanceReconciler) ensureBYOCNamespace(ctx context.Context, remote client.Client) error {
	var ns corev1.Namespace
	err := remote.Get(ctx, client.ObjectKey{Name: byocNamespace}, &ns)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   byocNamespace,
		Labels: map[string]string{"app.kubernetes.io/managed-by": "private-llm-operator"},
	}}
	if err := remote.Create(ctx, &ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// desiredAPIKeyFile assembles the llama.cpp --api-key-file content from the
// token Secrets of this instance. Before the first APITokenRequest exists the
// endpoint stays locked behind a generated bootstrap key, which is kept
// stable across reconciles to avoid needless pod restarts.
func (r *LLMInstanceReconciler) desiredAPIKeyFile(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string) (string, bool, error) {
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.InNamespace(inst.Namespace), client.MatchingLabels{instanceLabelKey: inst.Name}); err != nil {
		return "", false, err
	}
	keys := make([]string, 0, len(secrets.Items))
	for _, s := range secrets.Items {
		if v, ok := s.Data[openAIAPIKeyKey]; ok && len(v) > 0 {
			keys = append(keys, string(v))
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		return strings.Join(keys, "\n") + "\n", false, nil
	}

	var existing corev1.Secret
	err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: byocKeySecretName(slug)}, &existing)
	if err == nil && existing.Annotations[byocBootstrapAnnotation] == byocBootstrapTrue && len(existing.Data[byocAPIKeyFileKey]) > 0 {
		return string(existing.Data[byocAPIKeyFileKey]), true, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return "", false, err
	}
	token, err := generateToken(32)
	if err != nil {
		return "", false, err
	}
	return token + "\n", true, nil
}

// reconcileBYOCKeySecret ensures the remote api-key Secret and returns its
// resourceVersion, which the Deployment carries as a pod template annotation
// so that key changes roll the pods without putting key-derived data into
// object metadata.
func (r *LLMInstanceReconciler) reconcileBYOCKeySecret(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug, keyFile string, bootstrap bool) (string, error) {
	logger := log.FromContext(ctx)
	name := byocKeySecretName(slug)
	desiredAnnotations := map[string]string{}
	if bootstrap {
		desiredAnnotations[byocBootstrapAnnotation] = byocBootstrapTrue
	}

	var existing corev1.Secret
	err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, &existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", err
		}
		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   byocNamespace,
				Labels:      byocLabels(inst.Name, slug),
				Annotations: desiredAnnotations,
			},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{byocAPIKeyFileKey: keyFile},
		}
		if err := remote.Create(ctx, &secret); err != nil {
			return "", err
		}
		logger.Info("created BYOC api-key Secret", "name", name)
		return secret.ResourceVersion, nil
	}

	updated := false
	if string(existing.Data[byocAPIKeyFileKey]) != keyFile {
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		existing.Data[byocAPIKeyFileKey] = []byte(keyFile)
		updated = true
	}
	if existing.Annotations[byocBootstrapAnnotation] != desiredAnnotations[byocBootstrapAnnotation] {
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		if bootstrap {
			existing.Annotations[byocBootstrapAnnotation] = byocBootstrapTrue
		} else {
			delete(existing.Annotations, byocBootstrapAnnotation)
		}
		updated = true
	}
	if !updated {
		return existing.ResourceVersion, nil
	}
	if err := remote.Update(ctx, &existing); err != nil {
		return "", err
	}
	logger.Info("updated BYOC api-key Secret", "name", name)
	return existing.ResourceVersion, nil
}

func buildBYOCDeployment(inst *llmv1alpha1.LLMInstance, slug string, replicas int32, model modelSelection, keysRevision string) appsv1.Deployment {
	labels := byocLabels(inst.Name, slug)
	container := llamaServerContainer(model, "--api-key-file", byocAPIKeyFilePath())
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      "api-keys",
		MountPath: byocAPIKeyMountPath,
		ReadOnly:  true,
	})
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      byocBaseName(slug),
			Namespace: byocNamespace,
			Labels:    copyStringMap(labels),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrTo(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: copyStringMap(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      copyStringMap(labels),
					Annotations: map[string]string{byocAPIKeysRevAnnotation: keysRevision},
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{llamaInitContainer(model)},
					Containers:     []corev1.Container{container},
					Volumes: []corev1.Volume{
						llamaModelVolume(),
						{
							Name: "api-keys",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: byocKeySecretName(slug)},
							},
						},
					},
				},
			},
		},
	}
}

func (r *LLMInstanceReconciler) reconcileBYOCDeployment(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string, model modelSelection, keysRevision string) error {
	logger := log.FromContext(ctx)
	name := byocBaseName(slug)
	desiredReplicas := inst.Spec.Replicas
	if desiredReplicas <= 0 {
		desiredReplicas = 1
	}

	var existing appsv1.Deployment
	if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		deploy := buildBYOCDeployment(inst, slug, desiredReplicas, model, keysRevision)
		if err := remote.Create(ctx, &deploy); err != nil {
			return err
		}
		logger.Info("created BYOC llama.cpp Deployment", "name", name)
		return nil
	}

	replicasChanged, _ := ensureDeploymentReplicas(&existing, desiredReplicas)
	modelChanged := ensureDeploymentModel(&existing, model, "--api-key-file", byocAPIKeyFilePath())
	hashChanged := false
	if existing.Spec.Template.Annotations[byocAPIKeysRevAnnotation] != keysRevision {
		if existing.Spec.Template.Annotations == nil {
			existing.Spec.Template.Annotations = map[string]string{}
		}
		existing.Spec.Template.Annotations[byocAPIKeysRevAnnotation] = keysRevision
		hashChanged = true
	}
	if !replicasChanged && !modelChanged && !hashChanged {
		return nil
	}
	if err := remote.Update(ctx, &existing); err != nil {
		return err
	}
	logger.Info("updated BYOC llama.cpp Deployment", "name", name,
		"replicasChanged", replicasChanged, "modelChanged", modelChanged, "apiKeysChanged", hashChanged)
	return nil
}

func (r *LLMInstanceReconciler) reconcileBYOCService(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string) error {
	logger := log.FromContext(ctx)
	name := byocBaseName(slug)
	labels := byocLabels(inst.Name, slug)

	var existing corev1.Service
	err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, &existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		svc := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: byocNamespace,
				Labels:    copyStringMap(labels),
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeLoadBalancer,
				Selector: copyStringMap(labels),
				Ports: []corev1.ServicePort{{
					Name:       byocServicePortName,
					Port:       byocServicePort,
					TargetPort: intstr.FromInt(int(byocTargetPort)),
					Protocol:   corev1.ProtocolTCP,
				}},
			},
		}
		if err := remote.Create(ctx, &svc); err != nil {
			return err
		}
		logger.Info("created BYOC LoadBalancer Service", "name", name)
		return nil
	}

	updated := false
	if existing.Spec.Type != corev1.ServiceTypeLoadBalancer {
		existing.Spec.Type = corev1.ServiceTypeLoadBalancer
		updated = true
	}
	if !stringMapEqual(existing.Spec.Selector, labels) {
		existing.Spec.Selector = copyStringMap(labels)
		updated = true
	}
	if ensureBYOCServicePort(&existing) {
		updated = true
	}
	if !updated {
		return nil
	}
	if err := remote.Update(ctx, &existing); err != nil {
		return err
	}
	logger.Info("updated BYOC LoadBalancer Service", "name", name)
	return nil
}

func ensureBYOCServicePort(svc *corev1.Service) bool {
	if len(svc.Spec.Ports) == 0 {
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       byocServicePortName,
			Port:       byocServicePort,
			TargetPort: intstr.FromInt(int(byocTargetPort)),
			Protocol:   corev1.ProtocolTCP,
		}}
		return true
	}

	idx := 0
	for i := range svc.Spec.Ports {
		if svc.Spec.Ports[i].Name == byocServicePortName {
			idx = i
			break
		}
	}

	port := &svc.Spec.Ports[idx]
	updated := false
	if port.Name != byocServicePortName {
		port.Name = byocServicePortName
		updated = true
	}
	if port.Port != byocServicePort {
		port.Port = byocServicePort
		updated = true
	}
	if port.TargetPort.IntValue() != int(byocTargetPort) {
		port.TargetPort = intstr.FromInt(int(byocTargetPort))
		updated = true
	}
	if port.Protocol != corev1.ProtocolTCP {
		port.Protocol = corev1.ProtocolTCP
		updated = true
	}
	return updated
}

func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}

func (r *LLMInstanceReconciler) evaluateBYOCReadiness(ctx context.Context, remote client.Client, slug string) (bool, string, string, string, error) {
	name := byocBaseName(slug)

	var deploy appsv1.Deployment
	if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "", "DeploymentNotFound", fmt.Sprintf("Deployment %q not found on BYOC cluster", name), nil
		}
		return false, "", "", "", err
	}
	if ready, reason, message := deploymentReadiness(&deploy, name); !ready {
		return false, "", reason, message, nil
	}

	var svc corev1.Service
	if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: name}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "", "ServiceNotFound", fmt.Sprintf("Service %q not found on BYOC cluster", name), nil
		}
		return false, "", "", "", err
	}
	addr := loadBalancerAddress(&svc)
	if addr == "" {
		return false, "", "LoadBalancerPending", fmt.Sprintf("Service %q has no LoadBalancer address yet", name), nil
	}
	endpoint := fmt.Sprintf("http://%s:%d", addr, byocServicePort)

	// llama.cpp serves /health without an API key, so the gate works from outside.
	probeURL := endpoint + "/health"
	if err := r.runServiceHealthCheck(ctx, probeURL); err != nil {
		return false, endpoint, "HealthCheckFailed", fmt.Sprintf("probe failed for %s: %v", probeURL, err), nil
	}
	return true, endpoint, "Provisioned", "LLM instance is ready", nil
}

func loadBalancerAddress(svc *corev1.Service) string {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			return ing.IP
		}
		if ing.Hostname != "" {
			return ing.Hostname
		}
	}
	return ""
}

// cleanupBYOC removes the remote objects on instance deletion. Cleanup is
// best-effort: an unreachable or already-deleted BYOC cluster must not block
// deletion of the LLMInstance.
func (r *LLMInstanceReconciler) cleanupBYOC(ctx context.Context, inst *llmv1alpha1.LLMInstance) {
	logger := log.FromContext(ctx)
	slug := ""
	if anns := inst.GetAnnotations(); anns != nil {
		slug = strings.TrimSpace(anns[slugAnnotationKey])
	}
	if slug == "" {
		return
	}
	remote, reason, _, err := r.byocClient(ctx, inst, slug)
	if err != nil || remote == nil {
		logger.Info("skipping BYOC cleanup; target cluster not reachable", "reason", reason, "error", err)
	} else {
		objects := []client.Object{
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocBaseName(slug)}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocBaseName(slug)}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocKeySecretName(slug)}},
		}
		for _, obj := range objects {
			if err := remote.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete BYOC object (non-blocking)", "name", obj.GetName())
			}
		}
	}

	// Remove the cached credentials eagerly; ownerRef GC would also get them,
	// but not before the finalizer completes.
	cached := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: inst.Namespace, Name: byocCredentialsName(slug)}}
	if err := r.Delete(ctx, cached); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "failed to delete cached BYOC credentials (non-blocking)", "name", cached.Name)
	}
}

// findInstancesForSecret requeues instances when one of their token Secrets
// changes (api-key file content) or when their BYOC kubeconfig Secret appears.
func (r *LLMInstanceReconciler) findInstancesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	var instances llmv1alpha1.LLMInstanceList
	if err := r.List(ctx, &instances, client.InNamespace(secret.GetNamespace())); err != nil {
		return nil
	}
	labeledInstance := secret.GetLabels()[instanceLabelKey]
	requests := make([]reconcile.Request, 0, 1)
	for _, inst := range instances.Items {
		matchesToken := labeledInstance == inst.Name
		matchesKubeconfig := inst.IsBYOC() && inst.Spec.ClusterRef.KubeconfigSecretName == secret.GetName()
		if !matchesToken && !matchesKubeconfig {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: inst.Namespace, Name: inst.Name},
		})
	}
	return requests
}
