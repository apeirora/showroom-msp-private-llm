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
package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	slugKey               = "llm.privatellms.msp/slug"
	credentialInstanceKey = "llm.privatellms.msp/credential-instance-uid"
	credentialTargetKey   = "llm.privatellms.msp/credential-target"
)

type Server struct {
	// Use the API reader, not the manager cache: revocation must not rely on
	// eventually consistent Secret or APITokenRequest data.
	K8sClient client.Reader
}

// Start handles /auth/verify. Instance identity comes exclusively from the
// operator-managed forwardAuth URL, never from client-forwarded headers.
func (s *Server) Start(ctx context.Context, bindAddress string) error {
	srv := &http.Server{Addr: bindAddress, Handler: s.Handler(ctx)}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	return srv.ListenAndServe()
}

type instanceBinding struct {
	namespace, name, uid, slug string
}

func parseBinding(query url.Values) (instanceBinding, bool) {
	for _, key := range []string{"namespace", "instance", "uid", "slug"} {
		if len(query[key]) != 1 || strings.TrimSpace(query.Get(key)) == "" {
			return instanceBinding{}, false
		}
	}
	b := instanceBinding{query.Get("namespace"), query.Get("instance"), query.Get("uid"), query.Get("slug")}
	valid := len(validation.IsDNS1123Label(b.namespace)) == 0 &&
		len(validation.IsDNS1123Subdomain(b.name)) == 0 && len(validation.IsValidLabelValue(b.slug)) == 0
	return b, valid
}

// Handler returns the HTTP handler for the auth server. Useful for tests.
func (s *Server) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		binding, ok := parseBinding(r.URL.Query())
		if !ok {
			http.Error(w, "missing or invalid instance binding", http.StatusBadRequest)
			return
		}
		header := r.Header.Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if !strings.HasPrefix(header, "Bearer ") || token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		allowed, err := s.authorize(r.Context(), binding, token)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to validate instance token")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !allowed {
			w.Header().Set("WWW-Authenticate", "Bearer error=\"invalid_token\"")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s *Server) authorize(ctx context.Context, binding instanceBinding, token string) (bool, error) {
	var inst llmv1alpha1.LLMInstance
	if err := s.K8sClient.Get(ctx, client.ObjectKey{Namespace: binding.namespace, Name: binding.name}, &inst); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if string(inst.UID) != binding.uid || !inst.DeletionTimestamp.IsZero() || inst.IsBYOC() ||
		inst.Status.ActiveBYOCKubeconfigSecretName != "" ||
		strings.TrimSpace(inst.Annotations[slugKey]) != binding.slug {
		return false, nil
	}

	var secrets corev1.SecretList
	if err := s.K8sClient.List(ctx, &secrets, client.InNamespace(inst.Namespace),
		client.MatchingLabels{slugKey: binding.slug}); err != nil {
		return false, err
	}
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		// Reject unrelated keys before any owner lookups or cluster-wide scans.
		if subtle.ConstantTimeCompare(sec.Data["OPENAI_API_KEY"], []byte(token)) != 1 {
			continue
		}
		valid, err := s.ownsToken(ctx, &inst, sec)
		if err != nil {
			return false, err
		}
		if valid {
			return s.uniqueSlug(ctx, &inst, binding.slug)
		}
	}
	return false, nil
}

func (s *Server) uniqueSlug(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) (bool, error) {
	// Until slugs have an operator-owned persistence/migration contract, deny
	// ambiguous routes rather than choosing one of several tenant claims.
	var instances llmv1alpha1.LLMInstanceList
	if err := s.K8sClient.List(ctx, &instances); err != nil {
		return false, err
	}
	for _, other := range instances.Items {
		if strings.TrimSpace(other.Annotations[slugKey]) == slug &&
			(other.UID != inst.UID || other.Namespace != inst.Namespace || other.Name != inst.Name) {
			return false, nil
		}
	}

	return true, nil
}

func (s *Server) ownsToken(ctx context.Context, inst *llmv1alpha1.LLMInstance, sec *corev1.Secret) (bool, error) {
	// The route UID alone does not bind a credential: a recreated instance or
	// a BYOC-to-hosted transition can create its route before token rotation.
	// Legacy keys remain denied until the token controller binds them safely.
	if sec.Namespace != inst.Namespace || sec.Type != corev1.SecretTypeOpaque || !sec.DeletionTimestamp.IsZero() ||
		sec.Labels["llm.privatellms.msp/instance"] != inst.Name ||
		sec.Annotations[credentialInstanceKey] != string(inst.UID) || sec.Annotations[credentialTargetKey] != "" {
		return false, nil
	}
	owner := metav1.GetControllerOf(sec)
	if owner == nil || owner.UID == "" || owner.Kind != "APITokenRequest" ||
		owner.APIVersion != llmv1alpha1.GroupVersion.String() {
		return false, nil
	}
	var tr llmv1alpha1.APITokenRequest
	if err := s.K8sClient.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: owner.Name}, &tr); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return tr.UID == owner.UID && tr.DeletionTimestamp.IsZero() && tr.Spec.InstanceName == inst.Name &&
		tr.Status.SecretName == sec.Name && tr.Status.ObservedGeneration == tr.Generation &&
		sec.Labels["llm.privatellms.msp/apitokenrequest"] == tr.Name, nil
}
