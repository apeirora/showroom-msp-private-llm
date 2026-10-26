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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	foreignNamespace = "tenant-b"
	testSlug         = "victim-slug"
	testToken        = "synthetic-test-token"
	boundURL         = "/auth/verify?slug=victim-slug&namespace=tenant-a&instance=model&uid=instance-a"
)

func authFixture() (*llmv1alpha1.LLMInstance, *llmv1alpha1.APITokenRequest, *corev1.Secret) {
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
		Name: "model", Namespace: "tenant-a", UID: "instance-a",
		Annotations: map[string]string{"llm.privatellms.msp/slug": testSlug},
	}}
	tr := &llmv1alpha1.APITokenRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: inst.Namespace, UID: "request-a", Generation: 1},
		Spec:       llmv1alpha1.APITokenRequestSpec{InstanceName: inst.Name},
		Status:     llmv1alpha1.APITokenRequestStatus{SecretName: "access-token", Phase: "Ready", ObservedGeneration: 1},
	}
	controller := true
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: tr.Status.SecretName, Namespace: inst.Namespace,
			Labels: map[string]string{"llm.privatellms.msp/slug": testSlug, "llm.privatellms.msp/instance": inst.Name,
				"llm.privatellms.msp/apitokenrequest": tr.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: llmv1alpha1.GroupVersion.String(), Kind: "APITokenRequest",
				Name: tr.Name, UID: tr.UID, Controller: &controller}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"OPENAI_API_KEY": []byte(testToken)},
	}
	return inst, tr, sec
}

func authClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	sch := runtime.NewScheme()
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := llmv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return clientfake.NewClientBuilder().WithScheme(sch).WithObjects(objects...).Build()
}

func authStatus(c client.Client) int {
	req := httptest.NewRequest(http.MethodGet, boundURL, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rr := httptest.NewRecorder()
	(&Server{K8sClient: c}).Handler(context.Background()).ServeHTTP(rr, req)
	return rr.Code
}

func TestAuthRejectsCrossTenantSecrets(t *testing.T) {
	for _, attack := range []string{"colliding instance", "foreign owned token without collision", "related BYOC secret"} {
		t.Run(attack, func(t *testing.T) {
			inst, tr, sec := authFixture()
			tr.Namespace, sec.Namespace = foreignNamespace, foreignNamespace
			objects := []client.Object{inst, tr, sec}
			switch attack {
			case "colliding instance":
				attacker := inst.DeepCopy()
				attacker.Namespace, attacker.UID = foreignNamespace, types.UID("instance-b")
				objects = append(objects, attacker)
			case "related BYOC secret":
				sec.Name = "byoc-kubeconfig"
				sec.OwnerReferences = nil
			}
			if got := authStatus(authClient(t, objects...)); got != http.StatusUnauthorized {
				t.Fatalf("foreign token status = %d, want %d", got, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuthAcceptsOwnedToken(t *testing.T) {
	inst, tr, sec := authFixture()
	if got := authStatus(authClient(t, inst, tr, sec)); got != http.StatusOK {
		t.Fatalf("owned token status = %d, want %d", got, http.StatusOK)
	}
}

func TestAuthRejectsUntrustedOwnership(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*llmv1alpha1.LLMInstance, *llmv1alpha1.APITokenRequest, *corev1.Secret)
	}{
		{"no owner", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.OwnerReferences = nil
		}},
		{"non controller owner", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			no := false
			s.OwnerReferences[0].Controller = &no
		}},
		{"wrong owner kind", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.OwnerReferences[0].Kind = "LLMInstance"
		}},
		{"wrong owner group", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.OwnerReferences[0].APIVersion = "untrusted.example/v1alpha1"
		}},
		{"recreated owner", func(_ *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			tr.UID = "new-request"
		}},
		{"missing owner", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.OwnerReferences[0].Name = "missing"
		}},
		{"owner targets another instance", func(_ *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			tr.Spec.InstanceName = "another-model"
		}},
		{"unobserved request change", func(_ *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, _ *corev1.Secret) { tr.Generation++ }},
		{"unpublished secret", func(_ *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			tr.Status.SecretName = "another-secret"
		}},
		{"wrong secret type", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.Type = corev1.SecretTypeTLS
		}},
		{"wrong instance label", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.Labels["llm.privatellms.msp/instance"] = "another-model"
		}},
		{"wrong request label", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.Labels["llm.privatellms.msp/apitokenrequest"] = "another-request"
		}},
		{"wrong slug label", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			s.Labels["llm.privatellms.msp/slug"] = "another-slug"
		}},
		{"recreated instance", func(i *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			i.UID = "new-instance"
		}},
		{"instance slug changed", func(i *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			i.Annotations["llm.privatellms.msp/slug"] = "another-slug"
		}},
		{"deleting instance", func(i *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			now := metav1.Now()
			i.DeletionTimestamp = &now
			i.Finalizers = []string{"test/finalizer"}
		}},
		{"revoking request", func(_ *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, _ *corev1.Secret) {
			now := metav1.Now()
			tr.DeletionTimestamp = &now
			tr.Finalizers = []string{"test/finalizer"}
		}},
		{"deleting secret", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			now := metav1.Now()
			s.DeletionTimestamp = &now
			s.Finalizers = []string{"test/finalizer"}
		}},
		{"empty key", func(_ *llmv1alpha1.LLMInstance, _ *llmv1alpha1.APITokenRequest, s *corev1.Secret) {
			delete(s.Data, "OPENAI_API_KEY")
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			inst, tr, sec := authFixture()
			tt.mutate(inst, tr, sec)
			if got := authStatus(authClient(t, inst, tr, sec)); got != http.StatusUnauthorized {
				t.Fatalf("untrusted token status = %d, want %d", got, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuthRequiresFixedBinding(t *testing.T) {
	inst, tr, sec := authFixture()
	handler := (&Server{K8sClient: authClient(t, inst, tr, sec)}).Handler(context.Background())
	for _, target := range []string{
		"/auth/verify", "/auth/verify?slug=" + testSlug,
		boundURL + "&namespace=tenant-b", boundURL + "&slug=another-slug", boundURL + "&uid=another-uid",
		"/auth/verify?slug=invalid%2Fslug&namespace=tenant-a&instance=model&uid=instance-a",
		"/auth/verify?slug=victim-slug&namespace=tenant-a&instance=model&uid=",
	} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("X-Forwarded-Uri", "/llm/"+testSlug+"/v1/models?namespace=tenant-a&instance=model&uid=instance-a")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("unbound request status = %d, want 400", rr.Code)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, boundURL, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Forwarded-Uri", "/llm/attacker-slug/v1/models?namespace=tenant-b&instance=attacker&uid=attacker")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("forwarded header changed fixed binding: status = %d", rr.Code)
	}
}

func TestAuthRejectsAmbiguousRoutes(t *testing.T) {
	for _, namespace := range []string{"tenant-a", foreignNamespace} {
		t.Run(namespace, func(t *testing.T) {
			inst, tr, sec := authFixture()
			other := inst.DeepCopy()
			other.Name, other.Namespace, other.UID = "other", namespace, "other-instance"
			if got := authStatus(authClient(t, inst, tr, sec, other)); got != http.StatusUnauthorized {
				t.Fatalf("ambiguous route status = %d, want 401", got)
			}
		})
	}
}

func TestAuthBearerValidation(t *testing.T) {
	inst, tr, sec := authFixture()
	handler := (&Server{K8sClient: authClient(t, inst, tr, sec)}).Handler(context.Background())
	for _, header := range []string{"", "Bearer ", "Basic synthetic", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, boundURL, nil)
		req.Header.Set("Authorization", header)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized || rr.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("invalid bearer status = %d, challenge present = %v", rr.Code, rr.Header().Get("WWW-Authenticate") != "")
		}
	}
}

type checkedAuthClient struct {
	client.Client
	failAt string
}

func (c checkedAuthClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch obj.(type) {
	case *llmv1alpha1.LLMInstance:
		if c.failAt == "instance" {
			return errors.New("synthetic instance lookup error")
		}
	case *llmv1alpha1.APITokenRequest:
		if c.failAt == "request" {
			return errors.New("synthetic request lookup error")
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c checkedAuthClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	options := &client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(options)
	}
	switch list.(type) {
	case *corev1.SecretList:
		if options.Namespace != "tenant-a" {
			return errors.New("unscoped Secret lookup")
		}
		if c.failAt == "secret list" {
			return errors.New("synthetic secret list error")
		}
	case *llmv1alpha1.LLMInstanceList:
		if c.failAt == "instance list" {
			return errors.New("synthetic instance list error")
		}
	}
	return c.Client.List(ctx, list, opts...)
}

func TestAuthLookupErrorsFailClosed(t *testing.T) {
	for _, failAt := range []string{"", "instance", "request", "secret list", "instance list"} {
		t.Run(failAt, func(t *testing.T) {
			inst, tr, sec := authFixture()
			c := checkedAuthClient{Client: authClient(t, inst, tr, sec), failAt: failAt}
			want := http.StatusInternalServerError
			if failAt == "" {
				want = http.StatusOK
			}
			if got := authStatus(c); got != want {
				t.Fatalf("lookup status = %d, want %d", got, want)
			}
		})
	}
}

type countingAuthClient struct {
	client.Client
	instanceLists, requestGets int
}

func (c *countingAuthClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*llmv1alpha1.APITokenRequest); ok {
		c.requestGets++
		if key.Name == "broken-owner" {
			return errors.New("unrelated owner lookup failed")
		}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *countingAuthClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*llmv1alpha1.LLMInstanceList); ok {
		c.instanceLists++
	}
	return c.Client.List(ctx, list, opts...)
}

func TestAuthSkipsExpensiveLookupsForUnmatchedTokens(t *testing.T) {
	inst, tr, sec := authFixture()
	malformed := sec.DeepCopy()
	malformed.Name = "aaa-unrelated"
	malformed.OwnerReferences[0].Name = "broken-owner"
	malformed.Data["OPENAI_API_KEY"] = []byte("unrelated-key")
	c := &countingAuthClient{Client: authClient(t, inst, tr, sec, malformed)}
	req := httptest.NewRequest(http.MethodGet, boundURL, nil)
	req.Header.Set("Authorization", "Bearer junk")
	rr := httptest.NewRecorder()
	(&Server{K8sClient: c}).Handler(context.Background()).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || c.instanceLists != 0 || c.requestGets != 0 {
		t.Fatalf("junk token: status=%d, cluster lists=%d, owner gets=%d", rr.Code, c.instanceLists, c.requestGets)
	}
	if got := authStatus(c); got != http.StatusOK {
		t.Fatalf("unrelated broken owner denied valid token: %d", got)
	}
	if c.instanceLists != 1 || c.requestGets != 1 {
		t.Fatalf("valid token: cluster lists=%d, owner gets=%d", c.instanceLists, c.requestGets)
	}
}
