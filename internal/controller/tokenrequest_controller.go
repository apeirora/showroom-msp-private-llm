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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

const (
	openAIAPIKeyKey          = "OPENAI_API_KEY"
	openAIAPIURLKey          = "OPENAI_API_URL"
	compatibilityLabelKey    = "apeirora.eu/llm-api-compatibility"
	compatibilityLabelOpenAI = "openai"
	// secretUpdatedAtAnnotation is set on APITokenRequest when its secret is updated.
	// This triggers the sync agent to re-sync the APITokenRequest and its related secret.
	secretUpdatedAtAnnotation = "llm.privatellms.msp/secret-updated-at"
	// Track the Secret revision so a failed request touch is retried even when
	// the endpoint write already succeeded. Related Secrets are not watched by
	// the sync agent.
	tokenSecretVersionAnnotation  = "llm.privatellms.msp/token-secret-version"
	endpointUnavailableAnnotation = "llm.privatellms.msp/endpoint-unavailable"
	// Model keys belong to one remote cluster identity, independently of TLS
	// certificate renewals. A target that retained an old key must not retain
	// access to the replacement target.
	credentialTargetAnnotation   = "llm.privatellms.msp/credential-target"
	credentialInstanceAnnotation = "llm.privatellms.msp/credential-instance-uid"
	// A nonempty, reserved name prevents clients from choosing their default
	// backend when an endpoint is unavailable. Cached clients still need restart.
	unavailableAPIURL = "https://unavailable.invalid"
)

const apiTokenRequestFinalizer = "llm.privatellms.msp/apitokenrequest-finalizer"

//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=apitokenrequests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=apitokenrequests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=apitokenrequests/finalizers,verbs=update
// needs to read llminstances to validate reference
//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=llminstances,verbs=get;list;watch
// create and manage secrets containing tokens
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

type APITokenRequestReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *APITokenRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var tr llmv1alpha1.APITokenRequest
	if err := r.Get(ctx, req.NamespacedName, &tr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tr.DeletionTimestamp.IsZero() {
		return r.finalizeTokenRequest(ctx, &tr)
	}

	if res, handled := r.ensureFinalizer(ctx, &tr); handled {
		return res, nil
	}

	// Validate referenced instance exists in same namespace
	var inst llmv1alpha1.LLMInstance
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: tr.Spec.InstanceName}, &inst); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.invalidateTokenEndpoint(ctx, &tr, nil); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.updatePendingStatus(ctx, &tr, "InstanceNotFound", "Referenced LLMInstance not found"); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, client.IgnoreNotFound(err)
	}

	// Read slug from instance annotation (used to avoid exposing namespace)
	slug := ""
	if inst.Annotations != nil {
		slug = strings.TrimSpace(inst.Annotations[slugAnnotationKey])
	}

	ready := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
	instanceReady := ready != nil && ready.Status == metav1.ConditionTrue &&
		inst.Status.ObservedGeneration == inst.Generation && ready.ObservedGeneration == inst.Generation &&
		inst.DeletionTimestamp.IsZero()
	endpoint := strings.TrimSpace(inst.Status.Endpoint)
	// Keep the BYOC guard while an old remote target is being cleaned up, even
	// if a spec edit has already switched the requested mode to hosted.
	byocEndpoint := inst.IsBYOC() || inst.Status.ActiveBYOCKubeconfigSecretName != ""
	validEndpoint := !byocEndpoint || (inst.Status.ActiveBYOCTargetID != "" && validBYOCTokenEndpoint(endpoint))
	if slug == "" || endpoint == "" || !instanceReady || !validEndpoint {
		waitingFor := []string{}
		if slug == "" {
			waitingFor = append(waitingFor, "annotation "+slugAnnotationKey)
		}
		if endpoint == "" {
			waitingFor = append(waitingFor, "status.endpoint")
		}
		if !instanceReady {
			waitingFor = append(waitingFor, "current Ready condition")
		}
		if !validEndpoint {
			waitingFor = append(waitingFor, "verified HTTPS endpoint")
		}
		// This also handles retargeting a request away from a BYOC instance:
		// the previous endpoint must not survive while the new target is pending.
		if err := r.invalidateTokenEndpoint(ctx, &tr, &inst); err != nil {
			return ctrl.Result{}, err
		}
		message := fmt.Sprintf("Waiting for LLMInstance %q to populate %s", inst.Name, strings.Join(waitingFor, " and "))
		if err := r.updatePendingStatus(ctx, &tr, "InstanceNotReady", message); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	secretName := fmt.Sprintf("%s-token", tr.Name)
	if err := r.ensureSecret(ctx, &tr, &inst, secretName, slug, endpoint); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateReadyStatus(ctx, &tr, secretName); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *APITokenRequestReconciler) finalizeTokenRequest(ctx context.Context, tr *llmv1alpha1.APITokenRequest) (ctrl.Result, error) {
	if !ctrlutil.ContainsFinalizer(tr, apiTokenRequestFinalizer) {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)
	var secretList corev1.SecretList
	// Only this request's token Secrets (see createSecret); an unscoped list
	// would delete every Secret in the namespace, including other instances'
	// tokens and BYOC kubeconfig Secrets.
	if err := r.List(ctx, &secretList, client.InNamespace(tr.Namespace),
		client.MatchingLabels{"llm.privatellms.msp/apitokenrequest": tr.Name}); err == nil {
		for i := range secretList.Items {
			sec := secretList.Items[i]
			// Labels select candidates; only the exact controller owner can
			// authorize deletion. A tenant can copy labels onto another Secret.
			if !ownsTokenSecret(tr, &sec) {
				continue
			}
			if err := r.Delete(ctx, &sec, client.Preconditions{UID: &sec.UID, ResourceVersion: &sec.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete associated secret during finalizer cleanup (non-blocking)", "secret", sec.Name)
			}
		}
	} else {
		logger.Error(err, "failed to list associated secrets during finalizer cleanup (non-blocking)")
	}

	key := client.ObjectKeyFromObject(tr)
	for i := 0; i < 3; i++ {
		ctrlutil.RemoveFinalizer(tr, apiTokenRequestFinalizer)
		if err := r.Update(ctx, tr); err != nil {
			if apierrors.IsNotFound(err) {
				break
			}
			if apierrors.IsConflict(err) {
				var fresh llmv1alpha1.APITokenRequest
				if gerr := r.Get(ctx, key, &fresh); gerr != nil {
					break
				}
				*tr = fresh
				continue
			}
			return ctrl.Result{}, err
		}
		break
	}
	return ctrl.Result{}, nil
}

func (r *APITokenRequestReconciler) ensureFinalizer(ctx context.Context, tr *llmv1alpha1.APITokenRequest) (ctrl.Result, bool) {
	if ctrlutil.ContainsFinalizer(tr, apiTokenRequestFinalizer) {
		return ctrl.Result{}, false
	}
	ctrlutil.AddFinalizer(tr, apiTokenRequestFinalizer)
	if err := r.Update(ctx, tr); err != nil {
		log.FromContext(ctx).Error(err, "failed to add finalizer, will retry on next reconcile")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, true
	}
	return ctrl.Result{Requeue: true}, true
}

func (r *APITokenRequestReconciler) ensureSecret(ctx context.Context, tr *llmv1alpha1.APITokenRequest, inst *llmv1alpha1.LLMInstance, secretName, slug, endpoint string) error {
	logger := log.FromContext(ctx)
	var sec corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: tr.Namespace, Name: secretName}, &sec)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.createSecret(ctx, tr, inst, secretName, slug, endpoint)
		}
		return err
	}

	if !ownsTokenSecret(tr, &sec) {
		return fmt.Errorf("refusing to use token Secret %s/%s: unexpected controller owner or type", sec.Namespace, sec.Name)
	}
	rotated, err := rotateTokenForTarget(&sec, inst, true)
	if err != nil {
		return err
	}
	if r.applySecretMutations(&sec, inst.Name, slug, endpoint) || rotated {
		if err := r.Update(ctx, &sec); err != nil {
			return err
		}
		logger.Info("token Secret endpoint updated", "secret", secretName)
	}
	return r.touchAPITokenRequest(ctx, tr, sec.ResourceVersion)
}

func ownsTokenSecret(tr *llmv1alpha1.APITokenRequest, sec *corev1.Secret) bool {
	owner := metav1.GetControllerOf(sec)
	return tr.UID != "" && sec.Namespace == tr.Namespace && owner != nil &&
		owner.APIVersion == llmv1alpha1.GroupVersion.String() && owner.Kind == "APITokenRequest" &&
		owner.UID == tr.UID && owner.Name == tr.Name && sec.Type == corev1.SecretTypeOpaque
}

// validBYOCTokenEndpoint checks the URL contract. The instance reconciler is
// responsible for certificate verification and the health check before Ready.
func validBYOCTokenEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != byocServicePortName || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := u.Hostname()
	if u.Host != host && u.Host != host+":443" {
		return false
	}
	return net.ParseIP(host) == nil && len(validation.IsDNS1123Subdomain(host)) == 0 &&
		strings.Contains(host, ".") && !strings.HasSuffix(host, ".invalid")
}

// invalidateTokenEndpoint withdraws the route and rotates an existing key once
// a new BYOC target identity is established. Retaining status.secretName lets
// the sync agent deliver this state before the new target becomes Ready.
func (r *APITokenRequestReconciler) invalidateTokenEndpoint(ctx context.Context, tr *llmv1alpha1.APITokenRequest, inst *llmv1alpha1.LLMInstance) error {
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: tr.Namespace, Name: tr.Name + "-token"}, &sec); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !ownsTokenSecret(tr, &sec) {
		return fmt.Errorf("refusing to invalidate token Secret %s/%s: unexpected controller owner or type", sec.Namespace, sec.Name)
	}
	changed, err := rotateTokenForTarget(&sec, inst, false)
	if err != nil {
		return err
	}
	// A pending hosted retarget must retain the previous label until Ready
	// rotation examines it. Rewriting it first would erase the only remaining
	// legacy binding when UID annotations have not yet been backfilled.
	pendingBYOC := inst != nil && (inst.IsBYOC() || inst.Status.ActiveBYOCKubeconfigSecretName != "") && inst.Status.ActiveBYOCTargetID != ""
	if pendingBYOC && sec.Labels[instanceLabelKey] != inst.Name {
		if sec.Labels == nil {
			sec.Labels = map[string]string{}
		}
		sec.Labels[instanceLabelKey] = inst.Name
		changed = true
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	if string(sec.Data[openAIAPIURLKey]) != unavailableAPIURL {
		sec.Data[openAIAPIURLKey] = []byte(unavailableAPIURL)
		changed = true
	}
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	if sec.Annotations[endpointUnavailableAnnotation] != byocBootstrapTrue {
		sec.Annotations[endpointUnavailableAnnotation] = byocBootstrapTrue
		changed = true
	}
	if changed {
		if err := r.Update(ctx, &sec); err != nil {
			return err
		}
	}
	if err := r.touchAPITokenRequest(ctx, tr, sec.ResourceVersion); err != nil {
		return err
	}
	tr.Status.SecretName = sec.Name
	return nil
}

// rotateTokenForTarget is called only after verifying exact Secret ownership.
// Pending instances with no established target cannot authorize a new key.
// Hosted migration rotates only when the replacement hosted endpoint is Ready.
func rotateTokenForTarget(sec *corev1.Secret, inst *llmv1alpha1.LLMInstance, allowHosted bool) (bool, error) {
	if inst == nil {
		return false, nil
	}
	target := inst.Status.ActiveBYOCTargetID
	instanceUID := string(inst.UID)
	byoc := inst.IsBYOC() || inst.Status.ActiveBYOCKubeconfigSecretName != ""
	if byoc {
		if target == "" {
			return false, nil
		}
		if instanceUID == "" {
			return false, fmt.Errorf("cannot bind a BYOC key without an instance UID")
		}
	} else {
		if !allowHosted {
			return false, nil
		}
		target = ""
	}
	if !byoc && sec.Annotations[credentialTargetAnnotation] == "" && sec.Annotations[credentialInstanceAnnotation] == "" && legacyHostedBindingMatches(sec, inst) {
		// Preserve a legacy key only when the prior instance name and slug
		// still match. A retarget before the first upgrade reconcile, or an
		// ambiguous binding, must rotate rather than adopt the previous key.
		if instanceUID == "" {
			return false, nil
		}
		if sec.Annotations == nil {
			sec.Annotations = map[string]string{}
		}
		sec.Annotations[credentialInstanceAnnotation] = instanceUID
		return true, nil
	}
	if sec.Annotations[credentialTargetAnnotation] == target && sec.Annotations[credentialInstanceAnnotation] == instanceUID {
		return false, nil
	}
	token, err := generateToken(32)
	if err != nil {
		return false, err
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	if sec.Annotations == nil {
		sec.Annotations = map[string]string{}
	}
	sec.Data[openAIAPIKeyKey] = []byte(token)
	if target == "" {
		delete(sec.Annotations, credentialTargetAnnotation)
	} else {
		sec.Annotations[credentialTargetAnnotation] = target
	}
	sec.Annotations[credentialInstanceAnnotation] = instanceUID
	return true, nil
}

func legacyHostedBindingMatches(sec *corev1.Secret, inst *llmv1alpha1.LLMInstance) bool {
	slug := strings.TrimSpace(inst.Annotations[slugAnnotationKey])
	return sec.Labels[instanceLabelKey] == inst.Name && slug != "" && sec.Labels[slugAnnotationKey] == slug
}

// touchAPITokenRequest retries synchronization of an already-written Secret.
// Comparing its resource version also picks up separately requested key rotation.
func (r *APITokenRequestReconciler) touchAPITokenRequest(ctx context.Context, tr *llmv1alpha1.APITokenRequest, secretVersion string) error {
	if secretVersion != "" && tr.Annotations[tokenSecretVersionAnnotation] == secretVersion {
		return nil
	}
	if tr.Annotations == nil {
		tr.Annotations = map[string]string{}
	}
	tr.Annotations[secretUpdatedAtAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
	tr.Annotations[tokenSecretVersionAnnotation] = secretVersion
	return r.Update(ctx, tr)
}

func (r *APITokenRequestReconciler) createSecret(ctx context.Context, tr *llmv1alpha1.APITokenRequest, inst *llmv1alpha1.LLMInstance, secretName, slug, endpoint string) error {
	token, err := generateToken(32)
	if err != nil {
		return err
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: tr.Namespace,
			Labels: func() map[string]string {
				m := map[string]string{
					"app.kubernetes.io/name":              "llm-token",
					"llm.privatellms.msp/instance":        inst.Name,
					"llm.privatellms.msp/apitokenrequest": tr.Name,
					compatibilityLabelKey:                 compatibilityLabelOpenAI,
				}
				if slug != "" {
					m["llm.privatellms.msp/slug"] = slug
				}
				return m
			}(),
			Annotations: map[string]string{
				"llm.privatellms.msp/description": strings.TrimSpace(tr.Spec.Description),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			openAIAPIKeyKey: token,
			openAIAPIURLKey: endpoint,
		},
	}
	if inst.UID != "" {
		secret.Annotations[credentialInstanceAnnotation] = string(inst.UID)
	}
	if inst.IsBYOC() || inst.Status.ActiveBYOCKubeconfigSecretName != "" {
		if inst.Status.ActiveBYOCTargetID == "" || inst.UID == "" {
			return fmt.Errorf("cannot mint a BYOC token before its target identity is established")
		}
		secret.Annotations[credentialTargetAnnotation] = inst.Status.ActiveBYOCTargetID
		secret.Annotations[credentialInstanceAnnotation] = string(inst.UID)
	}
	if err := ctrl.SetControllerReference(tr, &secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, &secret); err != nil {
		return err
	}
	log.FromContext(ctx).Info("created token Secret", "name", secretName)
	return nil
}

func (r *APITokenRequestReconciler) applySecretMutations(sec *corev1.Secret, instanceName, slug, endpoint string) bool {
	updated := false
	if _, exists := sec.Annotations[endpointUnavailableAnnotation]; exists {
		delete(sec.Annotations, endpointUnavailableAnnotation)
		updated = true
	}
	if sec.Labels == nil {
		sec.Labels = map[string]string{}
	}
	if sec.Labels[instanceLabelKey] != instanceName {
		sec.Labels[instanceLabelKey] = instanceName
		updated = true
	}
	if sec.Labels[compatibilityLabelKey] != compatibilityLabelOpenAI {
		sec.Labels[compatibilityLabelKey] = compatibilityLabelOpenAI
		updated = true
	}
	if slug != "" && sec.Labels[slugAnnotationKey] != slug {
		sec.Labels[slugAnnotationKey] = slug
		updated = true
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	if val, ok := sec.Data[openAIAPIURLKey]; !ok || string(val) != endpoint {
		sec.Data[openAIAPIURLKey] = []byte(endpoint)
		updated = true
	}
	return updated
}

func (r *APITokenRequestReconciler) updateReadyStatus(ctx context.Context, tr *llmv1alpha1.APITokenRequest, secretName string) error {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Provisioned",
		Message:            "Token generated",
		LastTransitionTime: metav1.Now(),
	}
	setTRStatusCondition(tr, cond)
	tr.Status.SecretName = secretName
	tr.Status.Phase = "Ready"
	tr.Status.ObservedGeneration = tr.Generation
	return r.Status().Update(ctx, tr)
}

func (r *APITokenRequestReconciler) updatePendingStatus(ctx context.Context, tr *llmv1alpha1.APITokenRequest, reason, message string) error {
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	setTRStatusCondition(tr, cond)
	tr.Status.Phase = "Pending"
	tr.Status.ObservedGeneration = tr.Generation
	return r.Status().Update(ctx, tr)
}

func (r *APITokenRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.APITokenRequest{}).
		Owns(&corev1.Secret{}).
		Watches(
			&llmv1alpha1.LLMInstance{},
			handler.EnqueueRequestsFromMapFunc(r.findTokenRequestsForInstance),
		).
		Complete(r)
}

func (r *APITokenRequestReconciler) findTokenRequestsForInstance(ctx context.Context, obj client.Object) []reconcile.Request {
	inst, ok := obj.(*llmv1alpha1.LLMInstance)
	if !ok {
		return nil
	}

	var tokenRequests llmv1alpha1.APITokenRequestList
	if err := r.List(ctx, &tokenRequests, client.InNamespace(inst.Namespace)); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(tokenRequests.Items))
	for _, tr := range tokenRequests.Items {
		if strings.TrimSpace(tr.Spec.InstanceName) != inst.Name {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      tr.Name,
				Namespace: tr.Namespace,
			},
		})
	}
	return requests
}

func setTRStatusCondition(tr *llmv1alpha1.APITokenRequest, cond metav1.Condition) {
	// replace existing same type condition
	if tr.Status.Conditions == nil {
		tr.Status.Conditions = make([]metav1.Condition, 0, 1)
	}
	out := tr.Status.Conditions[:0]
	for _, c := range tr.Status.Conditions {
		if c.Type == cond.Type {
			continue
		}
		out = append(out, c)
	}
	out = append(out, cond)
	tr.Status.Conditions = out
}

func generateToken(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// URL-safe, no padding
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "="), nil
}
