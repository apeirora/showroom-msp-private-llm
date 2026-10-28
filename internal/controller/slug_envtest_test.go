package controller

import (
	"context"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Slug migration against the Kubernetes API", func() {
	It("preserves a reviewed legacy endpoint and refuses a stale identity status patch", func() {
		ctx := context.Background()
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "slug-migration-" + utilrand.String(5)}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, namespace)).To(Succeed()) })
		old := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: namespace.Name, Annotations: map[string]string{slugAnnotationKey: "Kept_Existing_Slug"}}}
		Expect(k8sClient.Create(ctx, old)).To(Succeed())
		policy := testLegacySlugPolicy(old)
		old.Status.Endpoint = "https://inference.example.test/llm/Kept_Existing_Slug"
		old.Status.Phase = slugReadyPhase
		Expect(k8sClient.Status().Update(ctx, old)).To(Succeed())
		r := &LLMInstanceReconciler{Client: k8sClient, RouteReader: k8sClient, Scheme: k8sClient.Scheme(), SlugPolicy: policy}
		slug, requeue, err := r.ensureSlug(ctx, old)
		Expect(err).NotTo(HaveOccurred())
		Expect(requeue).To(BeTrue())
		Expect(slug).To(Equal("Kept_Existing_Slug"))
		var persisted llmv1alpha1.LLMInstance
		key := client.ObjectKeyFromObject(old)
		Expect(k8sClient.Get(ctx, key, &persisted)).To(Succeed())
		Expect(persisted.Status.Slug).To(Equal(slug))
		Expect(persisted.Status.SlugSource).To(Equal(testLegacySlugSource))
		Expect(persisted.Status.Endpoint).To(Equal(old.Status.Endpoint))
		Expect(persisted.Status.Phase).To(Equal(slugReadyPhase))
		// Retain the earlier resource version but force the migration write path.
		// A replacement must not receive the stale object's approved legacy slug.
		stale := old.DeepCopy()
		stale.Status.Slug = ""
		stale.Status.SlugSource = ""
		Expect(k8sClient.Delete(ctx, old)).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(k8sClient.Get(ctx, key, &llmv1alpha1.LLMInstance{})) }).Should(BeTrue())
		replacement := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: old.Name, Namespace: old.Namespace}}
		Expect(k8sClient.Create(ctx, replacement)).To(Succeed())
		Expect(replacement.UID).NotTo(Equal(old.UID))
		_, _, err = r.ensureSlug(ctx, stale)
		Expect(apierrors.IsConflict(err)).To(BeTrue())
		Expect(k8sClient.Get(ctx, key, replacement)).To(Succeed())
		Expect(replacement.Status.Slug).To(BeEmpty())
		Expect(replacement.Status.Endpoint).To(BeEmpty())
	})
})
