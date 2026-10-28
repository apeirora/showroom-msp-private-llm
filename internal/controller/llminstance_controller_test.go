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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

type createFailingClient struct {
	client.Client
	err error
}

const (
	testClusterB           = "cluster-b"
	testClusterBKubeconfig = "cluster-b-kubeconfig"
)

func (c createFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return c.err
}

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
			ServiceHealthChecker: func(_ context.Context, _ string) error {
				return nil
			},
		}

		DeferCleanup(func() {
			err := k8sClient.Delete(ctx, ns)
			if err != nil && !apierrors.IsNotFound(err) {
				Fail(err.Error())
			}
		})
	})

	It("creates resources, reports provisioning, then marks ready after health gates pass", func() {
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

		// third reconcile provisions resources, but should not mark ready yet
		res, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(provisioningRequeueAfter))

		deployName := fmt.Sprintf("%s-llama", name)
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &deploy)).To(Succeed())
		Expect(deploy.Spec.Replicas).NotTo(BeNil())
		Expect(*deploy.Spec.Replicas).To(Equal(int32(2)))
		Expect(deploy.Spec.Template.Spec.InitContainers[0].Args).To(Equal([]string{modelDownloadCommand(resolveModel("phi-2"))}))
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
		Expect(inst.Status.Phase).To(Equal("Provisioning"))
		ready := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))

		By("marking deployment ready and adding service endpoints")
		deploy.Status.ObservedGeneration = deploy.Generation
		deploy.Status.Replicas = 2
		deploy.Status.UpdatedReplicas = 2
		deploy.Status.ReadyReplicas = 2
		deploy.Status.AvailableReplicas = 2
		Expect(k8sClient.Status().Update(ctx, &deploy)).To(Succeed())

		endpoints := &corev1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deployName,
				Namespace: namespace,
			},
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.0.10"}},
				Ports:     []corev1.EndpointPort{{Port: 8000}},
			}},
		}
		Expect(k8sClient.Create(ctx, endpoints)).To(Succeed())

		// fourth reconcile should mark instance as ready
		res, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Requeue || res.RequeueAfter > 0).To(BeFalse())

		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.Status.Phase).To(Equal("Ready"))
		Expect(inst.Status.Endpoint).To(Equal(fmt.Sprintf("http://public.example.test/llm/%s", slug)))
		ready = meta.FindStatusCondition(inst.Status.Conditions, "Ready")
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
		initContainer := deploy.Spec.Template.Spec.InitContainers[0]
		Expect(initContainer.Args).To(Equal([]string{modelDownloadCommand(resolveModel("tinyllama"))}))
		container := deploy.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal(llamaServerImage))
		Expect(container.Env).To(ContainElement(corev1.EnvVar{Name: "MODEL_PATH", Value: "/models/tinyllama.gguf"}))
		Expect(container.Args).To(Equal([]string{"-m", "/models/tinyllama.gguf", "--port", "8000", "--host", "0.0.0.0"}))
	})

	DescribeTable("applies runtime settings through the actual reconcile path", func(byoc bool) {
		reconciler.ModelServerCeilings = ModelServerCeilings{CPUMilli: 6000, MemoryMiB: 8192, ContextSize: 16384, ParallelSlots: 4}
		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: namespace},
			Spec: llmv1alpha1.LLMInstanceSpec{
				Model: "qwen3-4b",
				Resources: &llmv1alpha1.ModelServerResources{
					CPU:    &llmv1alpha1.ModelCPUResources{RequestMilli: 6000, LimitMilli: 6000},
					Memory: &llmv1alpha1.ModelMemoryResources{RequestMiB: 4096, LimitMiB: 8192},
				},
				ContextSize: ptrTo(int32(16384)), ParallelSlots: ptrTo(int32(1)),
			},
		}
		var tlsFixture byocIntegrationFixture
		if byoc {
			tlsFixture = configureBYOCIntegration(reconciler)
			reconciler.RemoteClientBuilder = func(_ []byte) (client.Client, error) { return k8sClient, nil }
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "runtime-cluster", Namespace: namespace}, Data: map[string][]byte{"kubeconfig": []byte(tlsFixture.kubeconfig("runtime-cluster"))}})).To(Succeed())
			inst.Spec.ClusterRef = &llmv1alpha1.ClusterRef{KubeconfigSecretName: "runtime-cluster"}
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: namespace}}
		for i := 0; i < 3; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		if byoc {
			tlsFixture.provision(inst, k8sClient)
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		key := types.NamespacedName{Name: inst.Name + "-llama", Namespace: namespace}
		if byoc {
			key = types.NamespacedName{Name: byocBaseName(inst.Annotations[slugAnnotationKey]), Namespace: byocNamespace}
		}
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		c := deploy.Spec.Template.Spec.Containers[0]
		Expect(c.Resources.Requests.Cpu().MilliValue()).To(Equal(int64(6000)))
		Expect(c.Resources.Limits.Cpu().MilliValue()).To(Equal(int64(6000)))
		Expect(c.Resources.Requests.Memory().String()).To(Equal("4Gi"))
		Expect(c.Resources.Limits.Memory().String()).To(Equal("8Gi"))
		Expect(c.Args).To(ContainElements("--jinja", "--ctx-size", "16384", "--parallel", "1"))
		Expect(countString(c.Args, "--ctx-size")).To(Equal(1))
		if byoc {
			Expect(c.Args).To(ContainElements("--api-key-file", byocAPIKeyFilePath()))
		}

		// Updates must replace existing values, not only affect new deployments.
		inst.Spec.Resources.CPU = &llmv1alpha1.ModelCPUResources{RequestMilli: 2000, LimitMilli: 4000}
		inst.Spec.ContextSize = ptrTo(int32(8192))
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		c = deploy.Spec.Template.Spec.Containers[0]
		Expect(c.Resources.Requests.Cpu().MilliValue()).To(Equal(int64(2000)))
		Expect(c.Resources.Limits.Cpu().MilliValue()).To(Equal(int64(4000)))
		Expect(c.Args).To(ContainElement("8192"))
		Expect(c.Args).NotTo(ContainElement("16384"))

		// Removing overrides restores this model's original defaults.
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		inst.Spec.Resources = nil
		inst.Spec.ContextSize = nil
		inst.Spec.ParallelSlots = nil
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		c = deploy.Spec.Template.Spec.Containers[0]
		Expect(c.Resources.Requests.Cpu().MilliValue()).To(Equal(int64(500)))
		Expect(c.Resources.Limits.Cpu().MilliValue()).To(Equal(int64(1800)))
		Expect(c.Resources.Limits.Memory().String()).To(Equal("7Gi"))
		Expect(c.Args).NotTo(ContainElement("--parallel"))
		if byoc {
			Expect(c.Args).To(ContainElements("--api-key-file", byocAPIKeyFilePath()))
		}
	}, Entry("local", false), Entry("BYOC", true))

	DescribeTable("rejects tenant budgets before local or BYOC workload writes", func(byoc bool) {
		inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "ceiling", Namespace: namespace}, Spec: llmv1alpha1.LLMInstanceSpec{Model: "qwen3-4b", Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 6000, LimitMilli: 6000}}}}
		remoteReads := 0
		var tlsFixture byocIntegrationFixture
		if byoc {
			tlsFixture = configureBYOCIntegration(reconciler)
			reconciler.RemoteClientBuilder = func(_ []byte) (client.Client, error) { remoteReads++; return k8sClient, nil }
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ceiling-cluster", Namespace: namespace}, Data: map[string][]byte{"kubeconfig": []byte(tlsFixture.kubeconfig("runtime-cluster"))}})).To(Succeed())
			inst.Spec.ClusterRef = &llmv1alpha1.ClusterRef{KubeconfigSecretName: "ceiling-cluster"}
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())
		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: inst.Name, Namespace: namespace}}
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.Status.Phase).To(Equal("Rejected"))
		condition := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal("ModelSettingsRejected"))
		Expect(condition.Message).To(ContainSubstring("provider ceiling 500"))
		Expect(inst.Status.Endpoint).To(BeEmpty())
		Expect(inst.Finalizers).To(BeEmpty())
		Expect(remoteReads).To(BeZero())
		version := inst.ResourceVersion
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.ResourceVersion).To(Equal(version))

		reconciler.ModelServerCeilings.CPUMilli = 6000
		for i := 0; i < 3; i++ {
			_, err = reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		if byoc {
			tlsFixture.provision(inst, k8sClient)
		}
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		key := types.NamespacedName{Name: inst.Name + "-llama", Namespace: namespace}
		if byoc {
			key = types.NamespacedName{Name: byocBaseName(inst.Annotations[slugAnnotationKey]), Namespace: byocNamespace}
		}
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().MilliValue()).To(Equal(int64(6000)))
		version = deploy.ResourceVersion
		inst.Spec.Resources.CPU.LimitMilli = 7000
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.Status.Phase).To(Equal("Rejected"))
		Expect(k8sClient.Get(ctx, key, &deploy)).To(Succeed())
		Expect(deploy.ResourceVersion).To(Equal(version))
	}, Entry("local", false), Entry("BYOC", true))

	It("keeps model defaults and independent resource pairs", func() {
		for _, name := range []string{"tinyllama", "phi-2", "gemma-3-1b-it", "gemma-3-4b-it", "gemma-3-12b-it", "qwen3-4b"} {
			inst := &llmv1alpha1.LLMInstance{Spec: llmv1alpha1.LLMInstanceSpec{Model: name}}
			Expect(modelForInstance(inst)).To(Equal(resolveModel(name)))
		}
		inst := &llmv1alpha1.LLMInstance{Spec: llmv1alpha1.LLMInstanceSpec{Model: "qwen3-4b", Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 6000, LimitMilli: 6000}}}}
		c := llamaServerContainer(modelForInstance(inst))
		Expect(c.Resources.Limits.Memory().String()).To(Equal("7Gi"))
		inst.Spec.Resources.CPU = nil
		inst.Spec.Resources.Memory = &llmv1alpha1.ModelMemoryResources{RequestMiB: 8192, LimitMiB: 16384}
		c = llamaServerContainer(modelForInstance(inst))
		Expect(c.Resources.Limits.Cpu().MilliValue()).To(Equal(int64(1800)))
		Expect(c.Resources.Limits.Memory().String()).To(Equal("16Gi"))
	})

	It("rejects resource and server settings outside API bounds", func() {
		invalid := []llmv1alpha1.LLMInstanceSpec{
			{Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 99, LimitMilli: 100}}},
			{Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 100, LimitMilli: 16001}}},
			{Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 6000, LimitMilli: 5000}}},
			{Resources: &llmv1alpha1.ModelServerResources{Memory: &llmv1alpha1.ModelMemoryResources{RequestMiB: 127, LimitMiB: 128}}},
			{Resources: &llmv1alpha1.ModelServerResources{Memory: &llmv1alpha1.ModelMemoryResources{RequestMiB: 128, LimitMiB: 65537}}},
			{Resources: &llmv1alpha1.ModelServerResources{Memory: &llmv1alpha1.ModelMemoryResources{RequestMiB: 8192, LimitMiB: 4096}}},
			{ContextSize: ptrTo(int32(1023))}, {ContextSize: ptrTo(int32(131073))},
			{ParallelSlots: ptrTo(int32(0))}, {ParallelSlots: ptrTo(int32(17))},
		}
		for i, spec := range invalid {
			inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("invalid-%d", i), Namespace: namespace}, Spec: spec}
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, inst))).To(BeTrue(), "case %d", i)
		}
	})

	It("configures Qwen for OpenAI-compatible tool calls with bounded resources", func() {
		model := resolveModel("qwen3-4b")
		Expect(model.url).To(ContainSubstring("/resolve/34778e26c8fa5e8bc0daa2389a9f958cffb1aedd/"))
		downloadCommand := modelDownloadCommand(model)
		Expect(downloadCommand).To(ContainSubstring("sha256sum -c -"))
		Expect(downloadCommand).To(ContainSubstring("Qwen3-4B-Q4_K_M.gguf.partial"))
		Expect(downloadCommand).To(ContainSubstring("else rm -f /models/Qwen3-4B-Q4_K_M.gguf.partial; exit 1"))

		initContainer := llamaInitContainer(model)
		Expect(initContainer.Resources.Requests.StorageEphemeral().String()).To(Equal("4Gi"))
		Expect(initContainer.Resources.Limits.StorageEphemeral().String()).To(Equal("5Gi"))

		container := llamaServerContainer(model)
		Expect(container.Args).To(Equal([]string{
			"-m", "/models/Qwen3-4B-Q4_K_M.gguf", "--port", "8000", "--host", "0.0.0.0",
			"--ctx-size", "8192", "--jinja", "--reasoning-format", "deepseek", "--reasoning", "off", "--no-webui",
		}))
		Expect(container.Image).To(Equal(llamaServerImage))
		Expect(container.Resources.Requests.Memory().String()).To(Equal("4Gi"))
		Expect(container.Resources.Limits.Memory().String()).To(Equal("7Gi"))
	})

	It("defaults model pods to cluster DNS and reconciles an explicit node resolver", func() {
		instance := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "dns-policy", Namespace: namespace},
		}
		deployment := buildDeployment(instance, llamaLabels(instance.Name), 1, resolveModel("tinyllama"))
		Expect(deployment.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))

		instance.Spec.DNSPolicy = corev1.DNSDefault
		Expect(ensureDeploymentDNSPolicy(&deployment, effectiveDNSPolicy(instance))).To(BeTrue())
		Expect(deployment.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSDefault))
		Expect(ensureDeploymentDNSPolicy(&deployment, effectiveDNSPolicy(instance))).To(BeFalse())

		instance.Spec.DNSPolicy = ""
		Expect(ensureDeploymentDNSPolicy(&deployment, effectiveDNSPolicy(instance))).To(BeTrue())
		Expect(deployment.Spec.Template.Spec.DNSPolicy).To(Equal(corev1.DNSClusterFirst))
	})

	It("reconciles an existing deployment to and from Qwen", func() {
		legacy := resolveModel("tinyllama")
		qwen := resolveModel("qwen3-4b")
		instance := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "migration", Namespace: namespace}}
		deployment := buildDeployment(instance, llamaLabels(instance.Name), 1, legacy)
		deployment.Spec.Template.Spec.Containers[0].Image = "ghcr.io/ggml-org/llama.cpp:server-b7045"

		Expect(ensureDeploymentModel(&deployment, qwen)).To(BeTrue())
		container := deployment.Spec.Template.Spec.Containers[0]
		Expect(container.Image).To(Equal(llamaServerImage))
		Expect(container.Args).To(ContainElements("--jinja", "--reasoning-format", "deepseek"))
		Expect(container.Resources.Requests.Memory().String()).To(Equal("4Gi"))
		Expect(deployment.Spec.Template.Spec.InitContainers[0].Args[0]).To(ContainSubstring(qwen.sha256))

		Expect(ensureDeploymentModel(&deployment, legacy)).To(BeTrue())
		container = deployment.Spec.Template.Spec.Containers[0]
		Expect(container.Args).To(Equal([]string{"-m", legacy.path(), "--port", "8000", "--host", "0.0.0.0"}))
		Expect(container.Resources.Requests).To(BeEmpty())
		Expect(container.Resources.Limits).To(BeEmpty())
	})

	It("preserves BYOC authentication arguments while reconciling Qwen", func() {
		instance := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "byoc-migration", Namespace: namespace}}
		deployment := buildDeployment(instance, llamaLabels(instance.Name), 1, resolveModel("tinyllama"))
		keyArguments := []string{"--api-key-file", byocAPIKeyFilePath()}

		Expect(ensureDeploymentModel(&deployment, resolveModel("qwen3-4b"), keyArguments...)).To(BeTrue())
		Expect(deployment.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{
			"-m", "/models/Qwen3-4B-Q4_K_M.gguf", "--port", "8000", "--host", "0.0.0.0",
			"--ctx-size", "8192", "--jinja", "--reasoning-format", "deepseek", "--reasoning", "off", "--no-webui",
			"--api-key-file", byocAPIKeyFilePath(),
		}))
	})

	It("deploys verified BYOC HTTPS, renews certificates, preserves model auth and cleans up", func() {
		f := configureBYOCIntegration(reconciler)
		var probedURL, probedFingerprint, servedFingerprint string
		reconciler.BYOCHealthChecker = func(_ context.Context, endpoint, fingerprint string) error {
			probedURL, probedFingerprint = endpoint, fingerprint
			if servedFingerprint != "" && servedFingerprint != fingerprint {
				return fmt.Errorf("fixture is serving previous certificate")
			}
			return nil
		}
		reconciler.RemoteClientBuilder = func([]byte) (client.Client, error) { return k8sClient, nil }
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue())
		inst := f.newInstance(namespace, "customer-kubeconfig", "customer-api")
		firstRevision := f.provision(inst, k8sClient)
		servedFingerprint = firstRevision
		remoteName := byocBaseName(inst.Annotations[slugAnnotationKey])
		tlsName, hostname, err := byocTLSIdentity(inst, reconciler.BYOCTLS, inst.Status.ActiveBYOCTargetID)
		Expect(err).NotTo(HaveOccurred())
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName, Namespace: byocNamespace}, &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Spec.Containers).To(HaveLen(2))
		Expect(deploy.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"-m", "/models/phi-2.Q4_0.gguf", "--port", "8000", "--host", "127.0.0.1", "--api-key-file", "/keys/api-keys"}))
		Expect(deploy.Spec.Template.Spec.Containers[0].Ports).To(BeEmpty())
		Expect(deploy.Spec.Template.Spec.Containers[1].Image).To(Equal(byocProxyImage))
		Expect(deploy.Spec.Template.Annotations[byocTLSRevisionAnnotation]).To(Equal(firstRevision))
		Expect(deploy.OwnerReferences).To(BeEmpty())
		Expect(deploy.Annotations[byocOwnerUIDAnnotation]).To(Equal(string(inst.UID)))

		var keys corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName + "-keys", Namespace: byocNamespace}, &keys)).To(Succeed())
		bootstrap := string(keys.Data[byocAPIKeyFileKey])
		Expect(bootstrap).NotTo(BeEmpty())
		Expect(keys.Annotations[byocBootstrapAnnotation]).To(Equal("true"))
		initialKeyRevision := deploy.Spec.Template.Annotations[byocAPIKeysRevAnnotation]
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&keys), &keys)).To(Succeed())
		Expect(string(keys.Data[byocAPIKeyFileKey])).To(Equal(bootstrap))
		var svc corev1.Service
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName, Namespace: byocNamespace}, &svc)).To(Succeed())
		Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
		Expect(svc.Spec.Ports).To(HaveLen(1))
		Expect(svc.Spec.Ports[0].Port).To(Equal(int32(443)))
		Expect(svc.Spec.Ports[0].TargetPort.IntValue()).To(Equal(8443))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(BeEmpty())

		byocMarkDeploymentReady(k8sClient, inst)
		res := byocTestReconcile(reconciler, inst)
		Expect(res.RequeueAfter).To(Equal(30 * time.Second))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(Equal("https://" + hostname))
		Expect(inst.Status.Phase).To(Equal("Ready"))
		Expect(probedURL).To(Equal("https://" + hostname + "/health"))
		Expect(probedFingerprint).To(Equal(firstRevision))

		By("keeping an unchanged Ready deployment stable across periodic reconciliation")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deploy), &deploy)).To(Succeed())
		stableVersion, stableGeneration := deploy.ResourceVersion, deploy.Generation
		for i := 0; i < 3; i++ {
			Expect(byocTestReconcile(reconciler, inst).RequeueAfter).To(Equal(30 * time.Second))
		}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deploy), &deploy)).To(Succeed())
		Expect(deploy.ResourceVersion).To(Equal(stableVersion))
		Expect(deploy.Generation).To(Equal(stableGeneration))

		By("withdrawing Ready after a later verified health probe fails")
		reconciler.BYOCHealthChecker = func(context.Context, string, string) error {
			return fmt.Errorf("fixture certificate fingerprint mismatch")
		}
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(BeEmpty())
		Expect(meta.IsStatusConditionFalse(inst.Status.Conditions, "Ready")).To(BeTrue())
		reconciler.BYOCHealthChecker = func(_ context.Context, endpoint, fingerprint string) error {
			probedURL, probedFingerprint = endpoint, fingerprint
			if servedFingerprint != fingerprint {
				return fmt.Errorf("fixture is serving previous certificate")
			}
			return nil
		}
		byocTestReconcile(reconciler, inst)

		By("requiring an owned token request before accepting a model key")
		tr := &llmv1alpha1.APITokenRequest{ObjectMeta: metav1.ObjectMeta{Name: inst.Name + "-access", Namespace: namespace}, Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: inst.Name}}
		Expect(k8sClient.Create(ctx, tr)).To(Succeed())
		token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: tr.Name + "-token", Namespace: namespace, Annotations: map[string]string{credentialTargetAnnotation: inst.Status.ActiveBYOCTargetID, credentialInstanceAnnotation: string(inst.UID)}, Labels: map[string]string{instanceLabelKey: inst.Name, "llm.privatellms.msp/apitokenrequest": tr.Name}}, Type: corev1.SecretTypeOpaque, StringData: map[string]string{openAIAPIKeyKey: "fixture-user-token"}}
		Expect(ctrl.SetControllerReference(tr, token, scheme.Scheme)).To(Succeed())
		Expect(k8sClient.Create(ctx, token)).To(Succeed())
		tr.Status.SecretName = token.Name
		Expect(k8sClient.Status().Update(ctx, tr)).To(Succeed())
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&keys), &keys)).To(Succeed())
		Expect(string(keys.Data[byocAPIKeyFileKey])).To(Equal("fixture-user-token\n"))
		Expect(keys.Annotations).NotTo(HaveKey(byocBootstrapAnnotation))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deploy), &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Annotations[byocAPIKeysRevAnnotation]).NotTo(Equal(initialKeyRevision))

		By("converging model changes without removing the key file or loopback binding")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		inst.Spec.Model = "qwen3-4b"
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deploy), &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Spec.Containers[0].Args).To(ContainElements("/models/Qwen3-4B-Q4_K_M.gguf", "127.0.0.1", "--api-key-file", "/keys/api-keys"))
		byocMarkDeploymentReady(k8sClient, inst)
		byocTestReconcile(reconciler, inst)

		By("replacing the TLS pair and requiring the new rollout and served fingerprint")
		secondRevision := f.issueCertificate(inst)
		Expect(secondRevision).NotTo(Equal(firstRevision))
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&deploy), &deploy)).To(Succeed())
		Expect(deploy.Spec.Template.Annotations[byocTLSRevisionAnnotation]).To(Equal(secondRevision))
		Expect(deploy.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(BeEmpty())
		byocMarkDeploymentReady(k8sClient, inst)
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(BeEmpty(), "old served certificate must not satisfy renewed readiness")
		servedFingerprint = secondRevision
		byocTestReconcile(reconciler, inst)
		Expect(probedFingerprint).To(Equal(secondRevision))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&keys), &keys)).To(Succeed())
		Expect(string(keys.Data[byocAPIKeyFileKey])).To(Equal("fixture-user-token\n"))

		By("cleaning up using owned cached credentials after the source Secret disappears")
		var cache corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName + "-credentials", Namespace: namespace}, &cache)).To(Succeed())
		Expect(cache.OwnerReferences[0].UID).To(Equal(inst.UID))
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "customer-kubeconfig", Namespace: namespace}})).To(Succeed())
		Expect(k8sClient.Delete(ctx, inst)).To(Succeed())
		for i := 0; i < 8; i++ {
			byocTestReconcile(reconciler, inst)
		}
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst))).To(BeTrue())
		for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName, Namespace: byocNamespace}, obj))).To(BeTrue())
		}
		for _, secretName := range []string{remoteName + "-keys", tlsName} {
			Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: byocNamespace}, &corev1.Secret{}))).To(BeTrue())
		}
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cache), &cache))).To(BeTrue())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName + "-proxy", Namespace: byocNamespace}, &corev1.ConfigMap{}))).To(BeTrue())
		byocAssertManagedDeleted(f, tlsName)
	})

	DescribeTable("preserves cleanup coordinates after an operator TLS scope change", func(changeNamespace bool) {
		f := configureBYOCIntegration(reconciler)
		remote := byocTestRemote("scope-target")
		reconciler.RemoteClientBuilder = func([]byte) (client.Client, error) { return remote, nil }
		inst := f.newInstance(namespace, "scope-kubeconfig", "scope-api")
		f.provision(inst, remote)
		byocMarkDeploymentReady(remote, inst)
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		originalConfig := reconciler.BYOCTLS
		tlsName, _, err := byocTLSIdentity(inst, originalConfig, inst.Status.ActiveBYOCTargetID)
		Expect(err).NotTo(HaveOccurred())
		if changeNamespace {
			reconciler.BYOCTLS.Namespace = "replacement-tls-scope"
		} else {
			reconciler.BYOCTLS.Domain = "replacement.example.test"
		}
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
		Expect(err).To(MatchError(ContainSubstring("explicit migration is required")))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.Endpoint).To(BeEmpty())
		Expect(inst.Status.ActiveBYOCTLSDomain).To(Equal(originalConfig.Domain))
		Expect(inst.Status.ActiveBYOCTLSNamespace).To(Equal(originalConfig.Namespace))
		Expect(k8sClient.Delete(ctx, inst)).To(Succeed())
		for i := 0; i < 8; i++ {
			byocTestReconcile(reconciler, inst)
		}
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst))).To(BeTrue())
		reconciler.BYOCTLS = originalConfig
		byocAssertManagedDeleted(f, tlsName)
	}, Entry("domain", false), Entry("credential namespace", true))

	It("withdraws active BYOC readiness when the source kubeconfig is missing", func() {
		f := configureBYOCIntegration(reconciler)
		remote := byocTestRemote("remote-a")
		reconciler.RemoteClientBuilder = func([]byte) (client.Client, error) { return remote, nil }
		inst := f.newInstance(namespace, "customer-kubeconfig", "cluster-a")
		f.provision(inst, remote)
		byocMarkDeploymentReady(remote, inst)
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "customer-kubeconfig", Namespace: namespace}})).To(Succeed())
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.ActiveBYOCKubeconfigSecretName).To(Equal("customer-kubeconfig"))
		Expect(inst.Status.Endpoint).To(BeEmpty())
		Expect(meta.IsStatusConditionFalse(inst.Status.Conditions, "Ready")).To(BeTrue())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: byocCredentialsName(inst.Annotations[slugAnnotationKey]), Namespace: namespace}, &corev1.Secret{})).To(Succeed())
	})

	DescribeTable("cleans the previous BYOC target and rotates the TLS identity", func(replaceSameSecret bool) {
		f := configureBYOCIntegration(reconciler)
		remoteA, remoteB := byocTestRemote("remote-a"), byocTestRemote("remote-b")
		reconciler.RemoteClientBuilder = func(data []byte) (client.Client, error) {
			if string(data) == f.kubeconfig("cluster-a") {
				return remoteA, nil
			}
			return remoteB, nil
		}
		inst := f.newInstance(namespace, "cluster-a-kubeconfig", "cluster-a")
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testClusterBKubeconfig, Namespace: namespace}, StringData: map[string]string{byocKubeconfigKey: f.kubeconfig(testClusterB)}})).To(Succeed())
		f.provision(inst, remoteA)
		oldID := inst.Status.ActiveBYOCTargetID
		oldTLS, _, err := byocTLSIdentity(inst, reconciler.BYOCTLS, oldID)
		Expect(err).NotTo(HaveOccurred())
		expectedTarget := testClusterBKubeconfig
		if replaceSameSecret {
			var replacement corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-a-kubeconfig", Namespace: namespace}, &replacement)).To(Succeed())
			replacement.Data[byocKubeconfigKey] = []byte(f.kubeconfig(testClusterB))
			Expect(k8sClient.Update(ctx, &replacement)).To(Succeed())
			expectedTarget = "cluster-a-kubeconfig"
		} else {
			inst.Spec.ClusterRef.KubeconfigSecretName = testClusterBKubeconfig
			Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		}
		for i := 0; i < 8; i++ {
			byocTestReconcile(reconciler, inst)
		}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.ActiveBYOCKubeconfigSecretName).To(Equal(expectedTarget))
		Expect(inst.Status.ActiveBYOCTargetID).NotTo(Equal(oldID))
		remoteName := byocBaseName(inst.Annotations[slugAnnotationKey])
		for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
			Expect(apierrors.IsNotFound(remoteA.Get(ctx, types.NamespacedName{Name: remoteName, Namespace: byocNamespace}, obj))).To(BeTrue())
		}
		Expect(apierrors.IsNotFound(remoteA.Get(ctx, types.NamespacedName{Name: oldTLS, Namespace: byocNamespace}, &corev1.Secret{}))).To(BeTrue())
		byocAssertManagedDeleted(f, oldTLS)
		f.provision(inst, remoteB)
		Expect(remoteB.Get(ctx, types.NamespacedName{Name: remoteName, Namespace: byocNamespace}, &appsv1.Deployment{})).To(Succeed())
		var cached corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: remoteName + "-credentials", Namespace: namespace}, &cached)).To(Succeed())
		Expect(string(cached.Data[byocKubeconfigKey])).To(Equal(f.kubeconfig(testClusterB)))
	}, Entry("when clusterRef selects another Secret", false), Entry("when the same kubeconfig Secret selects another API", true))

	It("keeps target status durable and the endpoint withdrawn when the new target fails", func() {
		f := configureBYOCIntegration(reconciler)
		remoteA, remoteB := byocTestRemote("remote-a"), byocTestRemote("remote-b")
		failRemoteB := true
		reconciler.RemoteClientBuilder = func(data []byte) (client.Client, error) {
			if string(data) == f.kubeconfig("cluster-a") {
				return remoteA, nil
			}
			if failRemoteB {
				return createFailingClient{Client: remoteB, err: fmt.Errorf("remote create blocked")}, nil
			}
			return remoteB, nil
		}
		inst := f.newInstance(namespace, "cluster-a-kubeconfig", "cluster-a")
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testClusterBKubeconfig, Namespace: namespace}, StringData: map[string]string{byocKubeconfigKey: f.kubeconfig(testClusterB)}})).To(Succeed())
		f.provision(inst, remoteA)
		byocMarkDeploymentReady(remoteA, inst)
		byocTestReconcile(reconciler, inst)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		inst.Spec.ClusterRef.KubeconfigSecretName = testClusterBKubeconfig
		Expect(k8sClient.Update(ctx, inst)).To(Succeed())
		var lastErr error
		for i := 0; i < 8; i++ {
			_, lastErr = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
		}
		Expect(lastErr).To(MatchError("remote create blocked"))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.ActiveBYOCKubeconfigSecretName).To(Equal(testClusterBKubeconfig))
		Expect(inst.Status.Endpoint).To(BeEmpty())
		Expect(meta.IsStatusConditionFalse(inst.Status.Conditions, "Ready")).To(BeTrue())
		failRemoteB = false
		f.provision(inst, remoteB)
		Expect(remoteB.Get(ctx, types.NamespacedName{Name: byocBaseName(inst.Annotations[slugAnnotationKey]), Namespace: byocNamespace}, &appsv1.Deployment{})).To(Succeed())
	})

	It("does not activate an invalid kubeconfig and recovers with a verified target", func() {
		f := configureBYOCIntegration(reconciler)
		remote := byocTestRemote("remote-b")
		reconciler.RemoteClientBuilder = func(data []byte) (client.Client, error) {
			if _, err := verifiedBYOCRESTConfig(data); err != nil {
				return nil, err
			}
			return remote, nil
		}
		inst := f.newInstance(namespace, "invalid-kubeconfig", "unused")
		var secret corev1.Secret
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "invalid-kubeconfig", Namespace: namespace}, &secret)).To(Succeed())
		secret.Data[byocKubeconfigKey] = []byte("invalid")
		Expect(k8sClient.Update(ctx, &secret)).To(Succeed())
		for i := 0; i < 3; i++ {
			byocTestReconcile(reconciler, inst)
		}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(inst), inst)).To(Succeed())
		Expect(inst.Status.ActiveBYOCKubeconfigSecretName).To(BeEmpty())
		Expect(meta.FindStatusCondition(inst.Status.Conditions, "Ready").Reason).To(Equal("KubeconfigInvalid"))
		secret.Data[byocKubeconfigKey] = []byte(f.kubeconfig(testClusterB))
		Expect(k8sClient.Update(ctx, &secret)).To(Succeed())
		f.provision(inst, remote)
		Expect(inst.Status.ActiveBYOCKubeconfigSecretName).To(Equal("invalid-kubeconfig"))
	})

	It("rejects unowned legacy Services and removes extra plaintext ports from owned Services", func() {
		name := "inst-" + utilrand.String(5)
		slug := "legacy-" + utilrand.String(5)
		remoteName := byocBaseName(slug)
		inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name)}}
		legacy := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: remoteName, Namespace: byocNamespace}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "legacy", Port: 8000, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolUDP}, {Name: "extra-http", Port: 80, TargetPort: intstr.FromInt(8000)}}}}
		err := k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: byocNamespace}})
		Expect(err == nil || apierrors.IsAlreadyExists(err)).To(BeTrue())
		Expect(k8sClient.Create(ctx, legacy)).To(Succeed())
		Expect(reconciler.reconcileBYOCService(ctx, k8sClient, inst, slug)).NotTo(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(legacy), legacy)).To(Succeed())
		Expect(legacy.Spec.Ports).To(HaveLen(2))
		legacy.Annotations = byocOwnedAnnotations(inst)
		Expect(k8sClient.Update(ctx, legacy)).To(Succeed())
		Expect(reconciler.reconcileBYOCService(ctx, k8sClient, inst, slug)).To(Succeed())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(legacy), legacy)).To(Succeed())
		Expect(legacy.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
		Expect(legacy.Spec.Ports).To(HaveLen(1))
		Expect(legacy.Spec.Ports[0].Name).To(Equal("https"))
		Expect(legacy.Spec.Ports[0].Port).To(Equal(int32(443)))
		Expect(legacy.Spec.Ports[0].TargetPort.IntValue()).To(Equal(8443))
		Expect(legacy.Spec.Ports[0].Protocol).To(Equal(corev1.ProtocolTCP))
	})

	It("treats an empty kubeconfigSecretName as as-a-Service", func() {
		// Form-based clients (the portal) submit clusterRef with an empty
		// kubeconfigSecretName when the optional BYOC field is left blank.
		name := "inst-" + utilrand.String(5)
		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: llmv1alpha1.LLMInstanceSpec{
				Model:      "phi-2",
				Replicas:   1,
				ClusterRef: &llmv1alpha1.ClusterRef{KubeconfigSecretName: ""},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
		// finalizer, then slug, then as-a-Service provisioning
		for i := 0; i < 2; i++ {
			res, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Requeue).To(BeTrue())
		}
		res, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(provisioningRequeueAfter))

		// the workload lands on the local cluster, not a BYOC target
		deployName := fmt.Sprintf("%s-llama", name)
		var deploy appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: namespace}, &deploy)).To(Succeed())

		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		slug := inst.Annotations[slugAnnotationKey]
		Expect(slug).NotTo(BeEmpty())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: byocBaseName(slug), Namespace: byocNamespace}, &appsv1.Deployment{}))).To(BeTrue())
	})

	It("reports a missing BYOC kubeconfig Secret instead of erroring", func() {
		reconciler.RemoteClientBuilder = func(_ []byte) (client.Client, error) {
			return k8sClient, nil
		}

		name := "inst-" + utilrand.String(5)
		inst := &llmv1alpha1.LLMInstance{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: llmv1alpha1.LLMInstanceSpec{
				ClusterRef: &llmv1alpha1.ClusterRef{KubeconfigSecretName: "does-not-exist"},
			},
		}
		Expect(k8sClient.Create(ctx, inst)).To(Succeed())

		req := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
		for i := 0; i < 2; i++ {
			_, err := reconciler.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())
		}
		res, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(provisioningRequeueAfter))

		Expect(k8sClient.Get(ctx, req.NamespacedName, inst)).To(Succeed())
		Expect(inst.Status.Phase).To(Equal("Provisioning"))
		ready := meta.FindStatusCondition(inst.Status.Conditions, "Ready")
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("KubeconfigSecretMissing"))
	})
})

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
