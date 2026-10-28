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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	"github.com/apeirora/showroom-msp-private-llm/internal/auth"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestForwardAuthBinding(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "create"
		if existing {
			name = "upgrade existing"
		}
		t.Run(name, func(t *testing.T) {
			sch := runtime.NewScheme()
			if err := llmv1alpha1.AddToScheme(sch); err != nil {
				t.Fatal(err)
			}
			inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "tenant-a", UID: "instance-a"}}
			mw := &unstructured.Unstructured{}
			mw.SetGroupVersionKind(schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"})
			mw.SetName(authMiddlewareName("model-llama"))
			mw.SetNamespace(inst.Namespace)
			if err := ctrl.SetControllerReference(inst, mw, sch); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedMap(mw.Object, map[string]interface{}{"address": "http://auth/auth/verify?slug=demo", "trustForwardHeader": true}, "spec", "forwardAuth"); err != nil {
				t.Fatal(err)
			}
			builder := clientfake.NewClientBuilder().WithScheme(sch)
			if existing {
				builder = builder.WithObjects(mw)
			}
			c := builder.Build()
			r := &LLMInstanceReconciler{Client: c, Scheme: sch, AuthServiceURL: "http://auth/"}
			if err := r.reconcileForwardAuthMiddleware(context.Background(), inst, nil, "model-llama", "demo&namespace=attacker"); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(mw), mw); err != nil {
				t.Fatal(err)
			}
			addr, _, err := unstructured.NestedString(mw.Object, "spec", "forwardAuth", "address")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(addr)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if u.Path != "/auth/verify" || q.Get("namespace") != inst.Namespace || len(q["namespace"]) != 1 ||
				q.Get("instance") != inst.Name || q.Get("uid") != string(inst.UID) || q.Get("slug") != "demo&namespace=attacker" {
				t.Fatalf("incorrect auth binding: %s", addr)
			}
			trust, _, err := unstructured.NestedBool(mw.Object, "spec", "forwardAuth", "trustForwardHeader")
			if err != nil || trust {
				t.Fatal("middleware still trusts client forwarded headers")
			}
			before := mw.GetResourceVersion()
			if err := r.reconcileForwardAuthMiddleware(context.Background(), inst, nil, "model-llama", "demo&namespace=attacker"); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(mw), mw); err != nil {
				t.Fatal(err)
			}
			if mw.GetResourceVersion() != before {
				t.Fatal("unchanged middleware was rewritten")
			}
			inst.UID = "different-instance"
			if err := r.reconcileForwardAuthMiddleware(context.Background(), inst, nil, "model-llama", "demo"); err == nil {
				t.Fatal("adopted another instance's middleware")
			}
		})
	}
}

func TestTokenSecretRejectsAdoption(t *testing.T) {
	for _, wrongType := range []bool{false, true} {
		sch := runtime.NewScheme()
		if err := llmv1alpha1.AddToScheme(sch); err != nil {
			t.Fatal(err)
		}
		if err := corev1.AddToScheme(sch); err != nil {
			t.Fatal(err)
		}
		tr := &llmv1alpha1.APITokenRequest{ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: "tenant-a", UID: "request-a"}}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "access-token", Namespace: tr.Namespace}, Type: corev1.SecretTypeOpaque}
		if wrongType {
			sec.Type = corev1.SecretTypeTLS
			if err := ctrl.SetControllerReference(tr, sec, sch); err != nil {
				t.Fatal(err)
			}
		}
		c := clientfake.NewClientBuilder().WithScheme(sch).WithObjects(sec).Build()
		r := &APITokenRequestReconciler{Client: c, Scheme: sch}
		err := r.ensureSecret(context.Background(), tr, &llmv1alpha1.LLMInstance{}, sec.Name, "demo", "https://llm.test/llm/demo")
		if err == nil {
			t.Fatal("accepted preexisting untrusted token Secret")
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(sec), sec); err != nil {
			t.Fatal(err)
		}
		if len(sec.Data) != 0 || len(sec.Labels) != 0 {
			t.Fatal("modified untrusted token Secret")
		}
	}
}

var _ = Describe("Authentication boundary against the Kubernetes API", func() {
	It("accepts controller-minted tokens, rejects foreign Secrets and collisions, and revokes on deletion", func() {
		ctx := context.Background()
		ns := "auth-" + utilrand.String(5)
		foreignNS := "auth-foreign-" + utilrand.String(5)
		for _, name := range []string{ns, foreignNS} {
			obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, obj)).To(Succeed()) })
		}
		const slug = "api-integration-slug"
		inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
			Name: "model", Namespace: ns, Annotations: map[string]string{slugAnnotationKey: slug},
		}}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		setInstanceReadyStatus(inst, "https://llm.example.test/llm/"+slug)
		Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())
		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: ns},
			Spec:       llmv1alpha1.APITokenRequestSpec{InstanceName: inst.Name},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())
		reconciler := &APITokenRequestReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: tr.Name}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		var sec corev1.Secret
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: tr.Status.SecretName}, &sec)).To(Succeed())
		// Keep generated token bytes in memory; only assert on HTTP status.
		query := url.Values{"namespace": {ns}, "instance": {inst.Name}, "uid": {string(inst.UID)}, "slug": {slug}}
		handler := (&auth.Server{K8sClient: k8sClient}).Handler(ctx)
		status := func(token string) int {
			request := httptest.NewRequest(http.MethodGet, "/auth/verify?"+query.Encode(), nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Code
		}
		Expect(status(string(sec.Data[openAIAPIKeyKey]))).To(Equal(http.StatusOK))
		// During an upgrade, an unbound legacy key stays denied until the token
		// controller verifies its old name/slug binding and records this UID.
		legacyKey := string(sec.Data[openAIAPIKeyKey])
		delete(sec.Annotations, credentialInstanceAnnotation)
		Expect(k8sClient.Update(ctx, &sec)).To(Succeed())
		Expect(status(legacyKey)).To(Equal(http.StatusUnauthorized))
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&sec), &sec)).To(Succeed())
		Expect(sec.Annotations[credentialInstanceAnnotation]).To(Equal(string(inst.UID)))
		Expect(string(sec.Data[openAIAPIKeyKey])).To(Equal(legacyKey))
		Expect(status(legacyKey)).To(Equal(http.StatusOK))
		foreign := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "byoc-kubeconfig", Namespace: foreignNS, Labels: map[string]string{slugAnnotationKey: slug}},
			StringData: map[string]string{openAIAPIKeyKey: "synthetic-foreign-token"},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
		Expect(status("synthetic-foreign-token")).To(Equal(http.StatusUnauthorized))
		collision := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
			Name: "other", Namespace: foreignNS, Annotations: map[string]string{slugAnnotationKey: slug},
		}}
		Expect(k8sClient.Create(ctx, collision)).To(Succeed())
		Expect(status(string(sec.Data[openAIAPIKeyKey]))).To(Equal(http.StatusUnauthorized))
		Expect(k8sClient.Delete(ctx, collision)).To(Succeed())
		Expect(status(string(sec.Data[openAIAPIKeyKey]))).To(Equal(http.StatusOK))
		Expect(k8sClient.Delete(ctx, tr)).To(Succeed())
		Expect(status(string(sec.Data[openAIAPIKeyKey]))).To(Equal(http.StatusUnauthorized))
	})
})

type deploymentReadFailure struct{ client.Client }

func (c deploymentReadFailure) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*appsv1.Deployment); ok {
		return errors.New("synthetic deployment read failure")
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestExistingAuthUpgradesBeforeWorkloadGates(t *testing.T) {
	cases := []struct {
		name                                string
		existing, rejectSettings, wantError bool
	}{
		{"rejected settings", true, true, false},
		{"workload lookup failure", true, false, true},
		{"rejected new instance creates no route", false, true, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sch := runtime.NewScheme()
			if err := llmv1alpha1.AddToScheme(sch); err != nil {
				t.Fatal(err)
			}
			inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
				Name: "model", Namespace: "tenant-a", UID: "instance-a",
				Annotations: map[string]string{slugAnnotationKey: "demo"},
				Finalizers:  []string{"llm.privatellms.msp/llminstance-finalizer"},
			}}
			if tt.rejectSettings {
				inst.Spec.Model = tinyLlamaModel
				inst.Spec.ContextSize = ptrTo(int32(16384))
			}
			mw := &unstructured.Unstructured{}
			mw.SetGroupVersionKind(schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"})
			mw.SetName(authMiddlewareName("model-llama"))
			mw.SetNamespace(inst.Namespace)
			if err := ctrl.SetControllerReference(inst, mw, sch); err != nil {
				t.Fatal(err)
			}
			if err := unstructured.SetNestedMap(mw.Object, map[string]interface{}{
				"address": "http://auth/auth/verify?slug=demo", "trustForwardHeader": true,
				"preserveLocationHeader": true,
			}, "spec", "forwardAuth"); err != nil {
				t.Fatal(err)
			}
			builder := clientfake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(inst).WithObjects(inst)
			if tt.existing {
				builder = builder.WithObjects(mw)
			}
			c := builder.Build()
			r := &LLMInstanceReconciler{Client: deploymentReadFailure{Client: c}, Scheme: sch, AuthServiceURL: "http://auth"}
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
			if (err != nil) != tt.wantError {
				t.Fatalf("reconcile error=%v, want error=%v", err, tt.wantError)
			}
			err = c.Get(ctx, client.ObjectKeyFromObject(mw), mw)
			if !tt.existing {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("unexpected middleware after rejected settings: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			addr, _, err := unstructured.NestedString(mw.Object, "spec", "forwardAuth", "address")
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(addr)
			if err != nil || u.Query().Get("uid") != string(inst.UID) {
				t.Fatalf("auth binding not upgraded: %s", addr)
			}
			preserved, _, err := unstructured.NestedBool(mw.Object, "spec", "forwardAuth", "preserveLocationHeader")
			if err != nil || !preserved {
				t.Fatal("unrelated forwardAuth option was lost")
			}
		})
	}
}

var _ = Describe("Token retargeting across instances", func() {
	It("rotates the token for a new instance and rejects the old key on both instances", func() {
		ctx := context.Background()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "retarget-" + utilrand.String(5)}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ns)).To(Succeed()) })
		instances := []*llmv1alpha1.LLMInstance{}
		for _, name := range []string{"first", "second"} {
			inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns.Name, Annotations: map[string]string{slugAnnotationKey: ns.Name + "-" + name},
			}}
			Expect(k8sClient.Create(ctx, inst)).To(Succeed())
			setInstanceReadyStatus(inst, "https://llm.example.test/llm/"+inst.Annotations[slugAnnotationKey])
			Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())
			instances = append(instances, inst)
		}
		tr := &llmv1alpha1.APITokenRequest{ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: ns.Name},
			Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: instances[0].Name}}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())
		reconciler := &APITokenRequestReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tr)}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		var sec corev1.Secret
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: tr.Status.SecretName}, &sec)).To(Succeed())
		token := string(sec.Data[openAIAPIKeyKey])
		handler := (&auth.Server{K8sClient: k8sClient}).Handler(ctx)
		status := func(inst *llmv1alpha1.LLMInstance) int {
			query := url.Values{"namespace": {inst.Namespace}, "instance": {inst.Name}, "uid": {string(inst.UID)}, "slug": {inst.Annotations[slugAnnotationKey]}}
			request := httptest.NewRequest(http.MethodGet, "/auth/verify?"+query.Encode(), nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			return response.Code
		}
		Expect(status(instances[0])).To(Equal(http.StatusOK))
		Expect(status(instances[1])).To(Equal(http.StatusUnauthorized))
		tr.Spec.InstanceName = instances[1].Name
		Expect(k8sClient.Update(ctx, tr)).To(Succeed())
		Expect(status(instances[0])).To(Equal(http.StatusUnauthorized))
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(status(instances[0])).To(Equal(http.StatusUnauthorized))
		Expect(status(instances[1])).To(Equal(http.StatusUnauthorized))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&sec), &sec)).To(Succeed())
		rotated := string(sec.Data[openAIAPIKeyKey])
		Expect(rotated).NotTo(Equal(token))
		token = rotated
		Expect(status(instances[0])).To(Equal(http.StatusUnauthorized))
		Expect(status(instances[1])).To(Equal(http.StatusOK))
		Expect(sec.Labels[instanceLabelKey]).To(Equal(instances[1].Name))
	})
})
