package controller

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// These CRDs exercise real API-server UID, generation and status semantics.
// Gardener's external issuer/DNS controllers are represented explicitly by the
// fixture; issuing a test certificate does not claim live Gardener acceptance.
func byocGardenerTestCRDs() []*apiextensionsv1.CustomResourceDefinition {
	out := make([]*apiextensionsv1.CustomResourceDefinition, 0, 2)
	for _, resource := range []struct{ group, kind, plural, singular string }{
		{"cert.gardener.cloud", "Certificate", "certificates", "certificate"},
		{"dns.gardener.cloud", "DNSEntry", "dnsentries", "dnsentry"},
	} {
		preserve := true
		out = append(out, &apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: resource.plural + "." + resource.group},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: resource.group, Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: resource.kind, Plural: resource.plural, Singular: resource.singular},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true,
					Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
					Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec": {Type: "object", XPreserveUnknownFields: &preserve}, "status": {Type: "object", XPreserveUnknownFields: &preserve},
					}}},
				}},
			},
		})
	}
	return out
}

type byocIntegrationFixture struct {
	r  *LLMInstanceReconciler
	ca byocTLSTestAuthority
}

func configureBYOCIntegration(r *LLMInstanceReconciler) byocIntegrationFixture {
	ca := newBYOCTLSTestAuthority(GinkgoT())
	err := k8sClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})
	Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue())
	ns := "tls-" + utilrand.String(8)
	Expect(k8sClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
	r.BYOCTLS = BYOCTLSConfig{Domain: "byoc.example.test", Namespace: ns, IssuerName: "issuer", IssuerNamespace: "kube-system", DNSClass: "garden", roots: ca.roots}
	r.BYOCHealthChecker = func(context.Context, string, string) error { return nil }
	return byocIntegrationFixture{r: r, ca: ca}
}

func (f byocIntegrationFixture) kubeconfig(cluster string) string {
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.cert.Raw}))
	return fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: selected\ncontexts:\n- name: selected\n  context:\n    cluster: target\nclusters:\n- name: target\n  cluster:\n    server: https://%s.example.test\n    certificate-authority-data: %s\n", cluster, ca)
}

func byocTestRemote(uid string) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithStatusSubresource(&appsv1.Deployment{}, &corev1.Service{}).
		WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}).Build()
}

func byocTestReconcile(r *LLMInstanceReconciler, inst *llmv1alpha1.LLMInstance) ctrl.Result {
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	Expect(err == nil || errors.Is(err, ErrBYOCTLSNotReady)).To(BeTrue(), "unexpected BYOC reconciliation failure: %v", err)
	return res
}

func (f byocIntegrationFixture) issueCertificate(inst *llmv1alpha1.LLMInstance) string {
	ctx := context.Background()
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
	name, host, err := byocTLSIdentity(inst, f.r.BYOCTLS, inst.Status.ActiveBYOCTargetID)
	Expect(err).NotTo(HaveOccurred())
	cert := byocTLSObject(byocCertificateGVK, f.r.BYOCTLS.Namespace, name)
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cert), cert)).To(Succeed())
	cert.Object["status"] = map[string]interface{}{"state": "Ready", "observedGeneration": cert.GetGeneration()}
	Expect(k8sClient.Status().Update(ctx, cert)).To(Succeed())
	data, revision := f.ca.issue(GinkgoT(), host, nil)
	source := &corev1.Secret{}
	err = k8sClient.Get(ctx, client.ObjectKey{Namespace: f.r.BYOCTLS.Namespace, Name: name}, source)
	if apierrors.IsNotFound(err) {
		source = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.r.BYOCTLS.Namespace,
			Labels: map[string]string{byocTLSOwnerLabel: string(inst.UID), byocTLSCertificateLabel: string(cert.GetUID())}}, Type: corev1.SecretTypeTLS, Data: data}
		Expect(k8sClient.Create(ctx, source)).To(Succeed())
	} else {
		Expect(err).NotTo(HaveOccurred())
		source.Data = data
		Expect(k8sClient.Update(ctx, source)).To(Succeed())
	}
	return revision
}

func (f byocIntegrationFixture) provision(inst *llmv1alpha1.LLMInstance, remote client.Client) string {
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		byocTestReconcile(f.r, inst)
	}
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
	var svc corev1.Service
	Expect(remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: byocBaseName(inst.Status.Slug)}, &svc)).To(Succeed())
	// A syntactically public address is required; external probes are overridden.
	// No connection or DNS write to this address occurs in envtest.
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "8.8.8.8"}}
	Expect(remote.Status().Update(ctx, &svc)).To(Succeed())
	revision := f.issueCertificate(inst)
	byocTestReconcile(f.r, inst)
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
	return revision
}

func byocMarkDeploymentReady(remote client.Client, inst *llmv1alpha1.LLMInstance) {
	ctx := context.Background()
	var deploy appsv1.Deployment
	Expect(remote.Get(ctx, client.ObjectKey{Namespace: byocNamespace, Name: byocBaseName(inst.Status.Slug)}, &deploy)).To(Succeed())
	deploy.Status.ObservedGeneration = deploy.Generation
	deploy.Status.Replicas, deploy.Status.UpdatedReplicas, deploy.Status.ReadyReplicas, deploy.Status.AvailableReplicas = 1, 1, 1, 1
	Expect(remote.Status().Update(ctx, &deploy)).To(Succeed())
}

func byocAssertManagedDeleted(f byocIntegrationFixture, name string) {
	ctx := context.Background()
	for _, gvk := range []struct{ group, kind string }{{"cert.gardener.cloud", "Certificate"}, {"dns.gardener.cloud", "DNSEntry"}} {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(gvk.group + "/v1alpha1")
		obj.SetKind(gvk.kind)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: f.r.BYOCTLS.Namespace, Name: name}, obj))).To(BeTrue())
	}
	for _, suffix := range []string{"", "-last-valid"} {
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: f.r.BYOCTLS.Namespace, Name: name + suffix}, &corev1.Secret{}))).To(BeTrue())
	}
}

func (f byocIntegrationFixture) newInstance(namespace, secretName, cluster string) *llmv1alpha1.LLMInstance {
	ctx := context.Background()
	Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace}, StringData: map[string]string{byocKubeconfigKey: f.kubeconfig(cluster)}})).To(Succeed())
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "inst-" + utilrand.String(6), Namespace: namespace}, Spec: llmv1alpha1.LLMInstanceSpec{Model: "phi-2", Replicas: 1, ClusterRef: &llmv1alpha1.ClusterRef{KubeconfigSecretName: secretName}}}
	Expect(k8sClient.Create(ctx, inst)).To(Succeed())
	return inst
}
