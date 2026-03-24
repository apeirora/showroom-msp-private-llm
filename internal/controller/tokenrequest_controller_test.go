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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

var _ = Describe("APITokenRequest controller", func() {
	var (
		ctx        context.Context
		namespace  string
		reconciler *APITokenRequestReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "tokenreq-" + utilrand.String(5)

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		reconciler = &APITokenRequestReconciler{Client: k8sClient, Scheme: scheme.Scheme}

		DeferCleanup(func() {
			err := k8sClient.Delete(ctx, ns)
			if err != nil && !apierrors.IsNotFound(err) {
				Fail(err.Error())
			}
		})
	})

	It("should provision secret and update status when instance exists", func() {
		instanceName := "instance-" + utilrand.String(5)
		tokenName := "token-" + utilrand.String(5)
		slug := "slug-" + utilrand.String(5)

		expectedEndpoint := fmt.Sprintf("https://public.example.test/llm/%s", slug)

		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
				Annotations: map[string]string{
					slugAnnotationKey: slug,
				},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		setInstanceReadyStatus(inst, expectedEndpoint)
		Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())

		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.APITokenRequestSpec{
				InstanceName: instanceName,
				Description:  "demo",
			},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tokenName, Namespace: namespace}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeTrue())

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		Expect(tr.Finalizers).To(ContainElement("llm.privatellms.msp/apitokenrequest-finalizer"))

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue || result.RequeueAfter > 0).To(BeFalse())

		secretKey := types.NamespacedName{Name: tokenName + "-token", Namespace: namespace}
		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/apitokenrequest", tokenName))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/instance", instanceName))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/slug", slug))
		Expect(secret.Labels).To(HaveKeyWithValue(compatibilityLabelKey, compatibilityLabelOpenAI))
		Expect(secret.Data).To(HaveKey("OPENAI_API_KEY"))
		Expect(secret.Data["OPENAI_API_KEY"]).NotTo(BeEmpty())
		Expect(secret.Data).To(HaveKey(openAIAPIURLKey))
		Expect(string(secret.Data[openAIAPIURLKey])).To(Equal(expectedEndpoint))

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		Expect(tr.Status.SecretName).To(Equal(secret.Name))
		Expect(tr.Status.ObservedGeneration).To(Equal(tr.Generation))
		Expect(tr.Status.Conditions).NotTo(BeEmpty())
		ready := meta.FindStatusCondition(tr.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal("Provisioned"))
	})

	It("should report not ready while waiting for referenced instance", func() {
		tokenName := "token-" + utilrand.String(5)

		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: "missing-instance"},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tokenName, Namespace: namespace}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeTrue())

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(10 * time.Second))

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		ready := meta.FindStatusCondition(tr.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InstanceNotFound"))

		By("ensuring no secret is created")
		secretKey := types.NamespacedName{Name: tokenName + "-token", Namespace: namespace}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, secretKey, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("should wait for instance slug and endpoint before provisioning secret", func() {
		instanceName := "instance-" + utilrand.String(5)
		tokenName := "token-" + utilrand.String(5)
		slug := "slug-" + utilrand.String(5)
		expectedEndpoint := fmt.Sprintf("https://public.example.test/llm/%s", slug)

		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: instanceName},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tokenName, Namespace: namespace}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeTrue())

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(10 * time.Second))

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		ready := meta.FindStatusCondition(tr.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InstanceNotReady"))

		secretKey := types.NamespacedName{Name: tokenName + "-token", Namespace: namespace}
		Eventually(func() bool {
			err := k8sClient.Get(ctx, secretKey, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: namespace}, inst)).To(Succeed())
		if inst.Annotations == nil {
			inst.Annotations = map[string]string{}
		}
		inst.Annotations[slugAnnotationKey] = slug
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: namespace}, inst)).To(Succeed())
		setInstanceReadyStatus(inst, expectedEndpoint)
		Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue || result.RequeueAfter > 0).To(BeFalse())

		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
		Expect(string(secret.Data[openAIAPIURLKey])).To(Equal(expectedEndpoint))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/slug", slug))

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		ready = meta.FindStatusCondition(tr.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal("Provisioned"))
	})

	It("should backfill endpoint and slug on existing token secret", func() {
		instanceName := "instance-" + utilrand.String(5)
		tokenName := "token-" + utilrand.String(5)
		slug := "slug-" + utilrand.String(5)
		expectedEndpoint := fmt.Sprintf("https://public.example.test/llm/%s", slug)

		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
				Annotations: map[string]string{
					slugAnnotationKey: slug,
				},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		setInstanceReadyStatus(inst, expectedEndpoint)
		Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())

		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: instanceName},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tokenName, Namespace: namespace}}
		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue).To(BeTrue())

		secretName := tokenName + "-token"
		staleSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					compatibilityLabelKey:                 compatibilityLabelOpenAI,
					"llm.privatellms.msp/instance":        instanceName,
					"llm.privatellms.msp/apitokenrequest": tokenName,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				openAIAPIKeyKey: []byte("static-test-token"),
				openAIAPIURLKey: []byte(""),
			},
		}
		Expect(k8sClient.Create(ctx, staleSecret)).To(Succeed())

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue || result.RequeueAfter > 0).To(BeFalse())

		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &secret)).To(Succeed())
		Expect(string(secret.Data[openAIAPIURLKey])).To(Equal(expectedEndpoint))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/slug", slug))
		Expect(string(secret.Data[openAIAPIKeyKey])).To(Equal("static-test-token"))
	})

	It("should remove generated secret during deletion", func() {
		instanceName := "instance-" + utilrand.String(5)
		tokenName := "token-" + utilrand.String(5)
		slug := "slug-" + utilrand.String(5)

		expectedEndpoint := fmt.Sprintf("https://public.example.test/llm/%s", slug)

		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      instanceName,
				Namespace: namespace,
				Annotations: map[string]string{
					slugAnnotationKey: slug,
				},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		setInstanceReadyStatus(inst, expectedEndpoint)
		Expect(k8sClient.Status().Update(ctx, inst)).To(Succeed())

		tr := &llmv1alpha1.APITokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: instanceName},
		}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tokenName, Namespace: namespace}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		secretKey := types.NamespacedName{Name: tokenName + "-token", Namespace: namespace}
		Expect(k8sClient.Get(ctx, secretKey, &corev1.Secret{})).To(Succeed())

		Expect(k8sClient.Get(ctx, req.NamespacedName, tr)).To(Succeed())
		Expect(k8sClient.Delete(ctx, tr)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() bool {
			err := k8sClient.Get(ctx, secretKey, &corev1.Secret{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())

		Eventually(func() bool {
			err := k8sClient.Get(ctx, req.NamespacedName, &llmv1alpha1.APITokenRequest{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})
})

func setInstanceReadyStatus(inst *llmv1alpha1.LLMInstance, endpoint string) {
	inst.Status.Endpoint = endpoint
	inst.Status.ObservedGeneration = inst.Generation
	meta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: inst.Generation,
		Reason:             "Provisioned",
		Message:            "LLM instance is ready",
	})
}
