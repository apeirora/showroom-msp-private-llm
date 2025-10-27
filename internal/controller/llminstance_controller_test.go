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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/example/private-llm/api/v1alpha1"
)

var _ = Describe("LLMInstanceReconciler", func() {
	var (
		ctx        context.Context
		reconciler *LLMInstanceReconciler
		namespace  string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "llminst-" + utilrand.String(5)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		reconciler = &LLMInstanceReconciler{
			Client:         k8sClient,
			Scheme:         scheme.Scheme,
			PublicHost:     "public.example.test",
			AuthServiceURL: "https://auth.example.test",
		}

		DeferCleanup(func() {
			err := k8sClient.Delete(ctx, ns)
			if err != nil && !apierrors.IsNotFound(err) {
				Fail(err.Error())
			}
		})
	})

	It("creates slug, deployment, service, ingress, and updates status", func() {
		name := "inst-" + utilrand.String(5)
		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.LLMInstanceSpec{
				Model:    "phi-2",
				Replicas: 2,
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
		// first reconcile should add finalizer and requeue
		res, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Requeue).To(BeTrue())

		// second reconcile should create slug and requeue
		res, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Requeue).To(BeTrue())

		// fetch instance to ensure slug set
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		slug := inst.Annotations[slugAnnotationKey]
		Expect(slug).NotTo(BeEmpty())

		// third reconcile should provision resources
		res, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Requeue).To(BeFalse())

		deployName := fmt.Sprintf("%s-llama", name)
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &deploy)).To(Succeed())
		Expect(deploy.Spec.Replicas).NotTo(BeNil())
		Expect(*deploy.Spec.Replicas).To(Equal(int32(2)))
		Expect(deploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: "MODEL_PATH", Value: "/models/phi-2.Q4_0.gguf"}))

		var svc corev1.Service
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &svc)).To(Succeed())
		Expect(svc.Spec.Selector["llm.privatellms.msp/instance"]).To(Equal(name))

		var ing networkingv1.Ingress
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &ing)).To(Succeed())
		Expect(ing.Spec.Rules).NotTo(BeEmpty())
		Expect(ing.Spec.Rules[0].Host).To(Equal("public.example.test"))
		Expect(ing.Spec.Rules[0].HTTP.Paths[0].Path).To(Equal(fmt.Sprintf("/llm/%s", slug)))

		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.Status.Phase).To(Equal("Ready"))
		Expect(inst.Status.Endpoint).To(Equal(fmt.Sprintf("http://public.example.test/llm/%s", slug)))
		ready := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
	})

	It("updates deployment when spec changes", func() {
		name := "inst-" + utilrand.String(5)
		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.LLMInstanceSpec{
				Model:    "phi-2",
				Replicas: 1,
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		slug := inst.Annotations[slugAnnotationKey]
		Expect(slug).NotTo(BeEmpty())

		inst.Spec.Model = "tinyllama"
		inst.Spec.Replicas = 3
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		deployName := fmt.Sprintf("%s-llama", name)
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &deploy)).To(Succeed())
		Expect(*deploy.Spec.Replicas).To(Equal(int32(3)))
		// ensure model path updated
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Env).To(ContainElement(corev1.EnvVar{Name: "MODEL_PATH", Value: "/models/tinyllama.gguf"}))
		Expect(container.Args).To(Equal([]string{"-m", "/models/tinyllama.gguf", "--port", "8000", "--host", "0.0.0.0"}))
	})
})
