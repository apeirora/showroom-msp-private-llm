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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	byocServicePortName      = "https"
	byocServicePort          = int32(443)
	byocTargetPort           = int32(8443)
	byocAPIKeysRevAnnotation = "llm.privatellms.msp/api-keys-revision"
	byocBootstrapAnnotation  = "llm.privatellms.msp/bootstrap-key"
	byocBootstrapTrue        = "true"
	byocReasonMissingSecret  = "KubeconfigSecretMissing"
	byocReasonTargetChanging = "BYOCTargetChanging"
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
	cfg, err := verifiedBYOCRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	httpClient, err := byocAPIHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{Scheme: r.Scheme, HTTPClient: httpClient})
}

func (r *LLMInstanceReconciler) remoteClientForKubeconfig(kubeconfig []byte) (client.Client, error) {
	builder := r.RemoteClientBuilder
	if builder == nil {
		builder = r.defaultRemoteClient
	}
	return builder(kubeconfig)
}

// byocKubeconfig fetches the source BYOC kubeconfig from the user-supplied
// Secret. Cached credentials are used only by cleanup helpers.
func (r *LLMInstanceReconciler) byocKubeconfig(ctx context.Context, inst *llmv1alpha1.LLMInstance) ([]byte, string, string, error) {
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
	return nil, byocReasonMissingSecret, fmt.Sprintf("kubeconfig Secret %q not found", secretName), nil
}

// byocClient returns a client and the exact credential snapshot used to build
// it. Identity verification and caching must use this snapshot too. A nil client
// with a reason signals a user-fixable provisioning condition.
func (r *LLMInstanceReconciler) byocClient(ctx context.Context, inst *llmv1alpha1.LLMInstance) (client.Client, []byte, string, string, error) {
	kubeconfig, reason, message, err := r.byocKubeconfig(ctx, inst)
	if err != nil || kubeconfig == nil {
		return nil, nil, reason, message, err
	}
	remote, err := r.remoteClientForKubeconfig(kubeconfig)
	if err != nil {
		return nil, nil, "KubeconfigInvalid", fmt.Sprintf("cannot build client for BYOC cluster: %v", err), nil
	}
	return remote, kubeconfig, "", "", nil
}

func (r *LLMInstanceReconciler) cachedBYOCKubeconfig(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) ([]byte, error) {
	if slug == "" {
		return nil, nil
	}
	name := byocCredentialsName(slug)
	var cached corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: name}, &cached)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !metav1.IsControlledBy(&cached, inst) {
		return nil, fmt.Errorf("refusing foreign cached BYOC credentials")
	}
	kubeconfig := cached.Data[byocKubeconfigKey]
	if len(kubeconfig) == 0 {
		return nil, fmt.Errorf("cached BYOC credentials Secret %q has no %q key", name, byocKubeconfigKey)
	}
	return kubeconfig, nil
}

// ensureCachedCredentials keeps an operator-owned copy of the kubeconfig so
// that remote cleanup still has credentials after the sync agent removed the
// user-supplied Secret. Owned by the instance, so GC removes it afterwards.
func (r *LLMInstanceReconciler) ensureCachedCredentials(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string, kubeconfig []byte) error {
	name := byocCredentialsName(slug)
	var existing corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: name}, &existing)
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
	if !metav1.IsControlledBy(&existing, inst) {
		return fmt.Errorf("refusing foreign cached BYOC credentials")
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

func (r *LLMInstanceReconciler) cleanupChangedBYOCTarget(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	previous := inst.Status.ActiveBYOCKubeconfigSecretName
	if previous == "" {
		return nil
	}
	slug = safeBYOCSlug(inst, slug)
	oldData, err := r.cachedBYOCKubeconfig(ctx, inst, slug)
	if err != nil {
		return err
	}
	if len(oldData) == 0 {
		return fmt.Errorf("cached BYOC credentials missing for previous target")
	}
	oldID := inst.Status.ActiveBYOCTargetID
	if inst.IsBYOC() {
		data, _, _, err := r.byocKubeconfig(ctx, inst)
		if err != nil {
			return err
		}
		// Missing source credentials cannot authorize a target move. The regular
		// reconcile de-advertises the endpoint until access returns.
		if data == nil && previous == inst.Spec.ClusterRef.KubeconfigSecretName {
			return nil
		}
		if data != nil {
			oldAPI, err := byocTargetIdentity(oldData)
			if err != nil {
				return err
			}
			newAPI, err := byocTargetIdentity(data)
			if err != nil {
				return err
			}
			if previous == inst.Spec.ClusterRef.KubeconfigSecretName && oldAPI == newAPI {
				remote, err := r.remoteClientForKubeconfig(data)
				if err != nil {
					return err
				}
				currentID, err := byocClusterIdentity(ctx, remote, newAPI)
				if err != nil {
					return err
				}
				if oldID == "" || currentID == oldID {
					return nil
				}
			}
		}
	}
	// Stop advertising first, including on a same-name kubeconfig replacement.
	if err := r.updateProvisioningStatus(ctx, inst, "", byocReasonTargetChanging, "Removing the previous BYOC endpoint"); err != nil {
		return err
	}
	if oldID != "" {
		// DNS deletion must finish before the LoadBalancer address is released.
		if err := r.cleanupBYOCManagedTLS(ctx, inst, r.byocCleanupTLSConfig(inst), oldID); err != nil {
			return err
		}
	}
	remote, err := r.remoteClientForKubeconfig(oldData)
	if err != nil {
		return err
	}
	oldAPI, err := byocTargetIdentity(oldData)
	if err != nil {
		return err
	}
	actualID, err := byocClusterIdentity(ctx, remote, oldAPI)
	if err != nil {
		return err
	}
	if oldID != "" && actualID != oldID {
		return fmt.Errorf("previous BYOC cluster identity changed; refusing cleanup on replacement cluster")
	}
	if err := cleanupBYOCObjects(ctx, remote, inst, slug); err != nil {
		return err
	}
	if oldID != "" {
		if err := cleanupBYOCRemoteTLS(ctx, remote, inst, r.byocCleanupTLSConfig(inst), oldID); err != nil {
			return err
		}
	}
	inst.Status.ActiveBYOCKubeconfigSecretName = ""
	inst.Status.ActiveBYOCTargetID = ""
	inst.Status.ActiveBYOCTLSDomain = ""
	inst.Status.ActiveBYOCTLSNamespace = ""
	if err := r.updateProvisioningStatus(ctx, inst, "", byocReasonTargetChanging, "Previous BYOC target removed"); err != nil {
		return err
	}
	return r.deleteCachedBYOCCredentials(ctx, inst, slug)
}

func (r *LLMInstanceReconciler) reconcileBYOC(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string, model modelSelection) (result ctrl.Result, reconcileErr error) {
	defer func() {
		if reconcileErr != nil {
			if err := r.updateProvisioningStatus(ctx, inst, "", "BYOCReconcileFailed", "BYOC endpoint is unavailable; operator reconciliation must succeed before publishing HTTPS"); err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
			}
		}
	}()
	if (inst.Status.ActiveBYOCTLSDomain != "" && inst.Status.ActiveBYOCTLSDomain != r.BYOCTLS.Domain) || (inst.Status.ActiveBYOCTLSNamespace != "" && inst.Status.ActiveBYOCTLSNamespace != r.BYOCTLS.Namespace) {
		return ctrl.Result{}, fmt.Errorf("BYOC TLS domain or credential namespace changed; explicit migration is required")
	}
	if inst.Status.ActiveBYOCSlug != "" && inst.Status.ActiveBYOCSlug != slug {
		return ctrl.Result{}, fmt.Errorf("BYOC workload slug is immutable after provisioning")
	}
	remote, data, reason, message, err := r.byocClient(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	if remote == nil {
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, r.updateProvisioningStatus(ctx, inst, "", reason, message)
	}
	apiID, err := byocTargetIdentity(data)
	if err != nil {
		return ctrl.Result{}, err
	}
	targetID, err := byocClusterIdentity(ctx, remote, apiID)
	if err != nil {
		return ctrl.Result{}, err
	}
	tlsName, hostname, err := byocTLSIdentity(inst, r.BYOCTLS, targetID)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Keep the previous target's cleanup credentials until cleanup completes.
	// The source Secret can change after the outer target-change check.
	if inst.Status.ActiveBYOCTargetID != "" && inst.Status.ActiveBYOCTargetID != targetID {
		return ctrl.Result{}, fmt.Errorf("BYOC cluster identity changed")
	}
	// Persist this verified snapshot before status makes cleanup depend on it.
	if err := r.ensureCachedCredentials(ctx, inst, slug, data); err != nil {
		return ctrl.Result{}, err
	}
	if inst.Status.ActiveBYOCKubeconfigSecretName != inst.Spec.ClusterRef.KubeconfigSecretName || inst.Status.ActiveBYOCSlug == "" || inst.Status.ActiveBYOCTargetID == "" {
		inst.Status.ActiveBYOCKubeconfigSecretName = inst.Spec.ClusterRef.KubeconfigSecretName
		inst.Status.ActiveBYOCSlug = slug
		inst.Status.ActiveBYOCTargetID = targetID
		inst.Status.ActiveBYOCTLSDomain = r.BYOCTLS.Domain
		inst.Status.ActiveBYOCTLSNamespace = r.BYOCTLS.Namespace
		if err := r.updateProvisioningStatus(ctx, inst, "", byocReasonTargetChanging, "BYOC HTTPS is being provisioned"); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.ensureBYOCNamespace(ctx, remote); err != nil {
		return ctrl.Result{}, err
	}
	tlsRevision, err := r.reconcileBYOCWorkload(ctx, remote, inst, slug, model, targetID, tlsName, hostname)
	if errors.Is(err, ErrBYOCTLSNotReady) {
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, r.updateProvisioningStatus(ctx, inst, "", "BYOCTLSProvisioning", "Waiting for a valid managed BYOC certificate and endpoint")
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	ready, endpoint, reason, message, err := r.evaluateBYOCReadiness(ctx, remote, slug, hostname, tlsRevision)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, r.updateProvisioningStatus(ctx, inst, "", reason, message)
	}
	if err := r.updateReadyStatus(ctx, inst, endpoint); err != nil {
		return ctrl.Result{}, err
	}
	// Periodic source/remote certificate validation and observed health are required
	// even after Ready, including when the source Secret watch was missed.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
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
		owner := metav1.GetControllerOf(&s)
		if owner == nil || owner.Kind != "APITokenRequest" || owner.APIVersion != llmv1alpha1.GroupVersion.String() || s.Type != corev1.SecretTypeOpaque || !s.DeletionTimestamp.IsZero() {
			continue
		}
		var request llmv1alpha1.APITokenRequest
		if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: owner.Name}, &request); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", false, err
		}
		if request.UID != owner.UID || request.Spec.InstanceName != inst.Name || !request.DeletionTimestamp.IsZero() || request.Status.SecretName != s.Name {
			continue
		}
		if inst.Status.ActiveBYOCTargetID == "" || s.Annotations[credentialTargetAnnotation] != inst.Status.ActiveBYOCTargetID || s.Annotations[credentialInstanceAnnotation] != string(inst.UID) {
			continue
		}
		if v, ok := s.Data[openAIAPIKeyKey]; ok && len(v) > 0 && !strings.ContainsAny(string(v), "\r\n") {
			keys = append(keys, string(v))
		}
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		return strings.Join(keys, "\n") + "\n", false, nil
	}

	var existing corev1.Secret
	err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: byocKeySecretName(slug)}, &existing)
	if err == nil {
		if err := checkBYOCObjectOwner(&existing, inst); err != nil {
			return "", false, err
		}
	}
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
	desiredAnnotations := byocOwnedAnnotations(inst)
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

	if err := checkBYOCObjectOwner(&existing, inst); err != nil {
		return "", err
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

func buildBYOCDeployment(inst *llmv1alpha1.LLMInstance, slug string, replicas int32, model modelSelection, keysRevision, tlsSecret, tlsRevision string) appsv1.Deployment {
	labels := byocLabels(inst.Name, slug)
	container := llamaServerContainer(model, "--api-key-file", byocAPIKeyFilePath())
	container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
		Name:      "api-keys",
		MountPath: byocAPIKeyMountPath,
		ReadOnly:  true,
	})
	deploy := appsv1.Deployment{
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
					DNSPolicy:      effectiveDNSPolicy(inst),
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
	configureBYOCTLSDeployment(&deploy, inst, slug, tlsSecret, tlsRevision)
	corev1defaults(&deploy)
	return deploy
}

func (r *LLMInstanceReconciler) reconcileBYOCDeployment(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string, model modelSelection, keysRevision, tlsSecret, tlsRevision string) error {
	replicas := inst.Spec.Replicas
	if replicas <= 0 {
		replicas = 1
	}
	desired := buildBYOCDeployment(inst, slug, replicas, model, keysRevision, tlsSecret, tlsRevision)
	var existing appsv1.Deployment
	err := remote.Get(ctx, client.ObjectKeyFromObject(&desired), &existing)
	if apierrors.IsNotFound(err) {
		return remote.Create(ctx, &desired)
	}
	if err != nil {
		return err
	}
	if err := checkBYOCObjectOwner(&existing, inst); err != nil {
		return err
	}
	// Preserve the immutable selector, but completely converge the owned pod
	// template. Old sidecars, plaintext ports and subPath mounts cannot survive.
	desired.Spec.Selector = existing.Spec.Selector.DeepCopy()
	// API defaults must not cause an endless rollout on every health requeue.
	corev1defaults(&desired)
	if reflect.DeepEqual(existing.Spec.Template, desired.Spec.Template) && reflect.DeepEqual(existing.Spec.Replicas, desired.Spec.Replicas) && reflect.DeepEqual(existing.Spec.Strategy, desired.Spec.Strategy) {
		return nil
	}
	existing.Spec.Template = desired.Spec.Template
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Strategy = desired.Spec.Strategy
	return remote.Update(ctx, &existing)
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
				Name:        name,
				Namespace:   byocNamespace,
				Labels:      copyStringMap(labels),
				Annotations: byocOwnedAnnotations(inst),
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

	if err := checkBYOCObjectOwner(&existing, inst); err != nil {
		return err
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
	port := corev1.ServicePort{Name: byocServicePortName, Port: byocServicePort, TargetPort: intstr.FromInt(int(byocTargetPort)), Protocol: corev1.ProtocolTCP}
	for _, old := range svc.Spec.Ports {
		if old.Port == byocServicePort {
			port.NodePort = old.NodePort
			break
		}
	}
	desired := []corev1.ServicePort{port}
	if reflect.DeepEqual(svc.Spec.Ports, desired) {
		return false
	}
	svc.Spec.Ports = desired
	return true
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

func (r *LLMInstanceReconciler) evaluateBYOCReadiness(ctx context.Context, remote client.Client, slug, hostname, fingerprint string) (bool, string, string, string, error) {
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
	endpoint := "https://" + hostname

	// llama.cpp serves /health without an API key, so the gate works from outside.
	probeURL := endpoint + "/health"
	if err := r.runBYOCHealthCheck(ctx, probeURL, fingerprint); err != nil {
		return false, "", "HealthCheckFailed", fmt.Sprintf("probe failed for %s: %v", probeURL, err), nil
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

func cleanupBYOCObjects(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string) error {
	objects := []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocBaseName(slug)}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocBaseName(slug)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocKeySecretName(slug)}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: byocNamespace, Name: byocBaseName(slug) + "-proxy"}},
	}
	var errs []error
	pending := false
	for _, obj := range objects {
		if err := remote.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			if !apierrors.IsNotFound(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := checkBYOCObjectOwner(obj, inst); err != nil {
			errs = append(errs, err)
			continue
		}
		pending = true
		if err := remote.Delete(ctx, obj, client.Preconditions{UID: ptrTo(obj.GetUID())}); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete %s/%s: %w", obj.GetNamespace(), obj.GetName(), err))
		}
	}
	if pending {
		errs = append(errs, fmt.Errorf("%w: waiting for owned BYOC objects to disappear", ErrBYOCTLSNotReady))
	}
	return errors.Join(errs...)
}

func (r *LLMInstanceReconciler) deleteCachedBYOCCredentials(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	var cached corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: byocCredentialsName(slug)}, &cached); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(&cached, inst) {
		return fmt.Errorf("refusing to delete foreign cached credentials")
	}
	return client.IgnoreNotFound(r.Delete(ctx, &cached, client.Preconditions{UID: ptrTo(cached.UID)}))
}

func (r *LLMInstanceReconciler) cleanupBYOC(ctx context.Context, inst *llmv1alpha1.LLMInstance) error {
	slug := inst.Status.ActiveBYOCSlug
	if slug == "" && inst.Status.ActiveBYOCKubeconfigSecretName != "" {
		selection, err := r.SlugPolicy.Select(inst)
		if err != nil {
			return fmt.Errorf("legacy BYOC cleanup requires its reviewed slug mapping: %w", err)
		}
		slug = selection.Slug
	}
	targetID := inst.Status.ActiveBYOCTargetID
	if targetID != "" {
		if err := r.cleanupBYOCManagedTLS(ctx, inst, r.byocCleanupTLSConfig(inst), targetID); err != nil {
			return err
		}
	}
	if inst.Status.ActiveBYOCKubeconfigSecretName == "" {
		return nil
	}
	data, err := r.cachedBYOCKubeconfig(ctx, inst, slug)
	if err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("cannot clean BYOC without protected cached credentials")
	}
	remote, err := r.remoteClientForKubeconfig(data)
	if err != nil {
		return err
	}
	apiID, err := byocTargetIdentity(data)
	if err != nil {
		return err
	}
	actualID, err := byocClusterIdentity(ctx, remote, apiID)
	if err != nil {
		return err
	}
	if targetID != "" && actualID != targetID {
		return fmt.Errorf("refusing cleanup on replacement BYOC cluster")
	}
	if err := cleanupBYOCObjects(ctx, remote, inst, slug); err != nil {
		return err
	}
	if targetID != "" {
		if err := cleanupBYOCRemoteTLS(ctx, remote, inst, r.byocCleanupTLSConfig(inst), targetID); err != nil {
			return err
		}
	}
	return r.deleteCachedBYOCCredentials(ctx, inst, slug)
}

// findInstancesForSecret requeues instances when one of their token Secrets
// changes (api-key file content) or when their BYOC kubeconfig Secret appears.
func (r *LLMInstanceReconciler) findInstancesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	if r.BYOCTLS.Namespace != "" && secret.Namespace == r.BYOCTLS.Namespace {
		var instances llmv1alpha1.LLMInstanceList
		if err := r.List(ctx, &instances); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, inst := range instances.Items {
			if inst.IsBYOC() {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: inst.Namespace, Name: inst.Name}})
			}
		}
		return requests
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

func (r *LLMInstanceReconciler) byocCleanupTLSConfig(inst *llmv1alpha1.LLMInstance) BYOCTLSConfig {
	cfg := r.BYOCTLS
	if inst.Status.ActiveBYOCTLSDomain != "" {
		cfg.Domain = inst.Status.ActiveBYOCTLSDomain
	}
	if inst.Status.ActiveBYOCTLSNamespace != "" {
		cfg.Namespace = inst.Status.ActiveBYOCTLSNamespace
	}
	return cfg
}

func (r *LLMInstanceReconciler) reconcileBYOCWorkload(ctx context.Context, remote client.Client, inst *llmv1alpha1.LLMInstance, slug string, model modelSelection, targetID, tlsName, hostname string) (string, error) {
	// Replace all owned plaintext Service ports before certificate provisioning.
	// Never preserve a legacy HTTP route while waiting for issuance.
	if err := r.reconcileBYOCService(ctx, remote, inst, slug); err != nil {
		return "", err
	}
	if err := r.reconcileBYOCManagedCertificate(ctx, inst, r.BYOCTLS, targetID); err != nil {
		return "", err
	}
	var svc corev1.Service
	if err := remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: byocBaseName(slug)}, &svc); err != nil {
		return "", err
	}
	addr := loadBalancerAddress(&svc)
	if addr == "" {
		return "", fmt.Errorf("%w: waiting for BYOC LoadBalancer address", ErrBYOCTLSNotReady)
	}
	if err := r.reconcileBYOCDNS(ctx, inst, r.BYOCTLS, addr, targetID); err != nil {
		return "", err
	}
	tlsRevision, err := r.reconcileBYOCTLSSecret(ctx, remote, inst, r.BYOCTLS, targetID)
	if err != nil {
		return "", err
	}
	if err := r.reconcileBYOCProxyConfig(ctx, remote, inst, slug, hostname); err != nil {
		return "", err
	}
	keyFile, bootstrap, err := r.desiredAPIKeyFile(ctx, remote, inst, slug)
	if err != nil {
		return "", err
	}
	keysRevision, err := r.reconcileBYOCKeySecret(ctx, remote, inst, slug, keyFile, bootstrap)
	if err != nil {
		return "", err
	}
	if err := r.reconcileBYOCDeployment(ctx, remote, inst, slug, model, keysRevision, tlsName, tlsRevision); err != nil {
		return "", err
	}
	return tlsRevision, nil
}
