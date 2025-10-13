package controller

import (
	"context"
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

	llmv1alpha1 "github.com/example/private-llm/api/v1alpha1"
)

var _ = Describe("TokenRequest controller", func() {
	var (
		ctx        context.Context
		namespace  string
		reconciler *TokenRequestReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "tokenreq-" + utilrand.String(5)

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		reconciler = &TokenRequestReconciler{Client: k8sClient, Scheme: scheme.Scheme}

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

		tr := &llmv1alpha1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.TokenRequestSpec{
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
		Expect(tr.Finalizers).To(ContainElement("llm.privatellms.msp/tokenrequest-finalizer"))

		result, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Requeue || result.RequeueAfter > 0).To(BeFalse())

		secretKey := types.NamespacedName{Name: tokenName + "-token", Namespace: namespace}
		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, secretKey, &secret)).To(Succeed())
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/tokenrequest", tokenName))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/instance", instanceName))
		Expect(secret.Labels).To(HaveKeyWithValue("llm.privatellms.msp/slug", slug))
		Expect(secret.Data).To(HaveKey("OPENAI_API_KEY"))
		Expect(secret.Data["OPENAI_API_KEY"]).NotTo(BeEmpty())

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

		tr := &llmv1alpha1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.TokenRequestSpec{InstanceName: "missing-instance"},
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

	It("should remove generated secret during deletion", func() {
		instanceName := "instance-" + utilrand.String(5)
		tokenName := "token-" + utilrand.String(5)
		slug := "slug-" + utilrand.String(5)

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

		tr := &llmv1alpha1.TokenRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tokenName,
				Namespace: namespace,
			},
			Spec: llmv1alpha1.TokenRequestSpec{InstanceName: instanceName},
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
			err := k8sClient.Get(ctx, req.NamespacedName, &llmv1alpha1.TokenRequest{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})
})
