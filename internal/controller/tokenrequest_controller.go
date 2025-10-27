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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	llmv1alpha1 "github.com/example/private-llm/api/v1alpha1"
)

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
	logger := log.FromContext(ctx)

	var tr llmv1alpha1.APITokenRequest
	if err := r.Get(ctx, req.NamespacedName, &tr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	const finalizerName = "llm.privatellms.msp/apitokenrequest-finalizer"
	// Handle deletion: ensure associated Secret(s) are removed, then drop finalizer
	if !tr.DeletionTimestamp.IsZero() {
		if ctrlutil.ContainsFinalizer(&tr, finalizerName) {
			var secretList corev1.SecretList
			if err := r.List(ctx, &secretList, client.InNamespace(req.Namespace)); err == nil {
				for i := range secretList.Items {
					sec := secretList.Items[i]
					if err := r.Delete(ctx, &sec); err != nil && !apierrors.IsNotFound(err) {
						logger.Error(err, "failed to delete associated secret during finalizer cleanup (non-blocking)", "secret", sec.Name)
					}
				}
			} else {
				logger.Error(err, "failed to list associated secrets during finalizer cleanup (non-blocking)")
			}
			// Remove finalizer with conflict retries to avoid blocking deletion
			for i := 0; i < 3; i++ {
				ctrlutil.RemoveFinalizer(&tr, finalizerName)
				if err := r.Update(ctx, &tr); err != nil {
					if apierrors.IsNotFound(err) {
						break
					}
					if apierrors.IsConflict(err) {
						// reload latest and retry
						var fresh llmv1alpha1.TokenRequest
						if gerr := r.Get(ctx, req.NamespacedName, &fresh); gerr != nil {
							break
						}
						tr = fresh
						continue
					}
					// for other errors, return and let controller requeue
					return ctrl.Result{}, err
				}
				break
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present (best-effort, avoid blocking reconcile on errors)
	if !ctrlutil.ContainsFinalizer(&tr, finalizerName) {
		ctrlutil.AddFinalizer(&tr, finalizerName)
		if err := r.Update(ctx, &tr); err != nil {
			logger.Error(err, "failed to add finalizer, will retry on next reconcile")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Validate referenced instance exists in same namespace
	var inst llmv1alpha1.LLMInstance
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: tr.Spec.InstanceName}, &inst); err != nil {
		if apierrors.IsNotFound(err) {
			cond := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InstanceNotFound", Message: "Referenced LLMInstance not found", LastTransitionTime: metav1.Now()}
			setTRStatusCondition(&tr, cond)
			// Mark as Pending until the instance appears
			tr.Status.Phase = "Pending"
			tr.Status.ObservedGeneration = tr.Generation
			_ = r.Status().Update(ctx, &tr)
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, client.IgnoreNotFound(err)
	}

	// Read slug from instance annotation (used to avoid exposing namespace)
	slug := ""
	if inst.Annotations != nil {
		slug = strings.TrimSpace(inst.Annotations[slugAnnotationKey])
	}

	// Ensure Secret exists with token
	secretName := fmt.Sprintf("%s-token", tr.Name)
	var sec corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: secretName}, &sec)
	if err != nil {
		if apierrors.IsNotFound(err) {
			token, terr := generateToken(32)
			if terr != nil {
				return ctrl.Result{}, terr
			}
			sec = corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: req.Namespace,
					Labels: func() map[string]string {
						m := map[string]string{
							"app.kubernetes.io/name":              "llm-token",
							"llm.privatellms.msp/instance":        inst.Name,
							"llm.privatellms.msp/apitokenrequest": tr.Name,
							// keep legacy label during transition
							"llm.privatellms.msp/tokenrequest": tr.Name,
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
					"OPENAI_API_KEY": token,
				},
			}
			if err := ctrl.SetControllerReference(&tr, &sec, r.Scheme); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Create(ctx, &sec); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("created token Secret", "name", secretName)
		} else {
			return ctrl.Result{}, err
		}
	}

	// Ensure slug label is present on the Secret (for legacy/existing secrets)
	if slug != "" {
		updated := false
		if sec.Labels == nil {
			sec.Labels = map[string]string{}
		}
		if sec.Labels["llm.privatellms.msp/slug"] != slug {
			sec.Labels["llm.privatellms.msp/slug"] = slug
			updated = true
		}
		if updated {
			if err := r.Update(ctx, &sec); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Update status
	cond := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Provisioned", Message: "Token generated", LastTransitionTime: metav1.Now()}
	setTRStatusCondition(&tr, cond)
	tr.Status.SecretName = secretName
	tr.Status.Phase = "Ready"
	tr.Status.ObservedGeneration = tr.Generation
	if err := r.Status().Update(ctx, &tr); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *APITokenRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.APITokenRequest{}).
		Owns(&corev1.Secret{}).
		Complete(r)
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
