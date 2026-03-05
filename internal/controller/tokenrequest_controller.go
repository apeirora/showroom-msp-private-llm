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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
			_ = r.updatePendingStatus(ctx, &tr, "InstanceNotFound", "Referenced LLMInstance not found")
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, client.IgnoreNotFound(err)
	}

	// Read slug from instance annotation (used to avoid exposing namespace)
	slug := ""
	if inst.Annotations != nil {
		slug = strings.TrimSpace(inst.Annotations[slugAnnotationKey])
	}

	instanceReady := meta.IsStatusConditionTrue(inst.Status.Conditions, "Ready")
	endpoint := strings.TrimSpace(inst.Status.Endpoint)
	if slug == "" || endpoint == "" || !instanceReady {
		waitingFor := []string{}
		if slug == "" {
			waitingFor = append(waitingFor, "annotation "+slugAnnotationKey)
		}
		if endpoint == "" {
			waitingFor = append(waitingFor, "status.endpoint")
		}
		if !instanceReady {
			waitingFor = append(waitingFor, "Ready condition")
		}
		message := fmt.Sprintf("Waiting for LLMInstance %q to populate %s", inst.Name, strings.Join(waitingFor, " and "))
		_ = r.updatePendingStatus(ctx, &tr, "InstanceNotReady", message)
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
	if err := r.List(ctx, &secretList, client.InNamespace(tr.Namespace)); err == nil {
		for i := range secretList.Items {
			sec := secretList.Items[i]
			if err := r.Delete(ctx, &sec); err != nil && !apierrors.IsNotFound(err) {
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

	if r.applySecretMutations(&sec, slug, endpoint) {
		if err := r.Update(ctx, &sec); err != nil {
			return err
		}
		logger.Info("secret updated, touching APITokenRequest to trigger sync agent re-sync",
			"secret", secretName, "endpoint", endpoint)
		// Touch the APITokenRequest to trigger sync agent to re-sync.
		// This is needed because the sync agent doesn't watch for changes to related
		// resources (secrets). By updating the APITokenRequest, the sync agent will
		// re-sync it along with its related secret.
		if err := r.touchAPITokenRequest(ctx, tr); err != nil {
			logger.Error(err, "failed to touch APITokenRequest after secret update")
			// Don't fail the reconcile - the secret was updated successfully
		}
	}
	return nil
}

// touchAPITokenRequest updates an annotation on the APITokenRequest to trigger
// the sync agent to re-sync it and its related secret.
func (r *APITokenRequestReconciler) touchAPITokenRequest(ctx context.Context, tr *llmv1alpha1.APITokenRequest) error {
	if tr.Annotations == nil {
		tr.Annotations = map[string]string{}
	}
	tr.Annotations[secretUpdatedAtAnnotation] = time.Now().Format(time.RFC3339)
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
	if err := ctrl.SetControllerReference(tr, &secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, &secret); err != nil {
		return err
	}
	log.FromContext(ctx).Info("created token Secret", "name", secretName)
	return nil
}

func (r *APITokenRequestReconciler) applySecretMutations(sec *corev1.Secret, slug, endpoint string) bool {
	updated := false
	if sec.Labels == nil {
		sec.Labels = map[string]string{}
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
