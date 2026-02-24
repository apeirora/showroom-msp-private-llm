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
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	llmv1alpha1 "github.com/example/private-llm/api/v1alpha1"
)

// LLMInstanceReconciler reconciles a LLMInstance object
type LLMInstanceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	PublicHost     string
	AuthServiceURL string
	// PublicScheme controls the URL scheme published in status.endpoint (e.g., "http" or "https").
	PublicScheme string
	// ExtraIngressAnnotations are merged into managed Ingress annotations.
	ExtraIngressAnnotations map[string]string
	// TLSSecretName, when non-empty, configures spec.tls with this secret for PublicHost.
	TLSSecretName string
	// ServiceHealthChecker probes service endpoints before publishing Ready=True.
	// If nil, a default HTTP checker is used.
	ServiceHealthChecker ServiceHealthChecker
}

const slugAnnotationKey = "llm.privatellms.msp/slug"
const provisioningRequeueAfter = 5 * time.Second

type ServiceHealthChecker func(ctx context.Context, targetURL string) error

//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=llminstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=llminstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=llm.privatellms.msp,resources=llminstances/finalizers,verbs=update
// core resources
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// apps and services
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
// networking
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// traefik middleware
//+kubebuilder:rbac:groups=traefik.io,resources=middlewares,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the LLMInstance object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *LLMInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := r.startTracing(ctx)
	defer span.End()

	logger := log.FromContext(ctx)

	inst, found, err := r.getInstance(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return ctrl.Result{}, nil
	}

	// Handle deletion: best-effort cleanup and remove finalizer without blocking
	const finalizerName = "llm.privatellms.msp/llminstance-finalizer"
	if !inst.DeletionTimestamp.IsZero() {
		if ctrlutil.ContainsFinalizer(inst, finalizerName) {
			// Attempt to remove owned resources via ownerRefs GC; nothing to do explicitly
			// Remove finalizer with small retry window to avoid blocking deletion
			for i := 0; i < 3; i++ {
				ctrlutil.RemoveFinalizer(inst, finalizerName)
				if err := r.Update(ctx, inst); err != nil {
					if apierrors.IsNotFound(err) {
						break
					}
					if apierrors.IsConflict(err) {
						fresh, ok, gerr := r.getInstance(ctx, req.NamespacedName)
						if gerr != nil || !ok {
							break
						}
						inst = fresh
						continue
					}
					return ctrl.Result{}, err
				}
				break
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer is present (best-effort)
	if !ctrlutil.ContainsFinalizer(inst, finalizerName) {
		ctrlutil.AddFinalizer(inst, finalizerName)
		if err := r.Update(ctx, inst); err != nil {
			// Don't block reconcile; retry shortly
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		return ctrl.Result{Requeue: true}, nil
	}

	slug, requeue, err := r.ensureSlug(ctx, inst)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{Requeue: true}, nil
	}

	labels := llamaLabels(inst.Name)
	model := resolveModel(inst.Spec.Model)

	if err := r.reconcileDeployment(ctx, inst, labels, model); err != nil {
		return ctrl.Result{}, err
	}

	svcName := fmt.Sprintf("%s-llama", inst.Name)
	if err := r.reconcileService(ctx, inst, labels, svcName); err != nil {
		return ctrl.Result{}, err
	}

	pathPrefix := fmt.Sprintf("/llm/%s", slug)

	if err := r.reconcileStripMiddleware(ctx, inst, labels, svcName, pathPrefix); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileForwardAuthMiddleware(ctx, inst, labels, svcName, slug); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileIngress(ctx, inst, labels, svcName, pathPrefix); err != nil {
		return ctrl.Result{}, err
	}

	ready, reason, message, err := r.evaluateInstanceReadiness(ctx, inst, svcName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		if err := r.updateProvisioningStatus(ctx, inst, slug, reason, message); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("LLMInstance is still provisioning", "name", req.NamespacedName, "reason", reason)
		return ctrl.Result{RequeueAfter: provisioningRequeueAfter}, nil
	}

	if err := r.updateReadyStatus(ctx, inst, slug); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled LLMInstance", "name", req.NamespacedName)
	return ctrl.Result{}, nil
}

func (r *LLMInstanceReconciler) startTracing(ctx context.Context) (context.Context, trace.Span) {
	tracer := otel.Tracer("github.com/example/private-llm/internal/controller")
	ctx, span := tracer.Start(ctx, "LLMInstanceReconciler.Reconcile", trace.WithAttributes())
	logger := log.FromContext(ctx)
	if sc := span.SpanContext(); sc.IsValid() {
		logger = logger.WithValues(
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	}
	ctx = log.IntoContext(ctx, logger)
	return ctx, span
}

func (r *LLMInstanceReconciler) getInstance(ctx context.Context, name types.NamespacedName) (*llmv1alpha1.LLMInstance, bool, error) {
	inst := &llmv1alpha1.LLMInstance{}
	if err := r.Get(ctx, name, inst); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return inst, true, nil
}

func (r *LLMInstanceReconciler) ensureSlug(ctx context.Context, inst *llmv1alpha1.LLMInstance) (string, bool, error) {
	anns := inst.GetAnnotations()
	slug := ""
	if anns != nil {
		slug = strings.TrimSpace(anns[slugAnnotationKey])
	}
	if slug != "" {
		return slug, false, nil
	}

	newSlug, err := generateSlug(9)
	if err != nil {
		return "", false, err
	}
	if anns == nil {
		anns = map[string]string{}
	}
	anns[slugAnnotationKey] = newSlug
	inst.SetAnnotations(anns)
	if err := r.Update(ctx, inst); err != nil {
		return "", false, err
	}
	return newSlug, true, nil
}

func llamaLabels(instanceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "llama-cpp-server",
		"llm.privatellms.msp/instance": instanceName,
	}
}

type modelSelection struct {
	name string
	file string
	url  string
}

func (m modelSelection) path() string {
	return "/models/" + m.file
}

func resolveModel(requested string) modelSelection {
	trimmed := strings.TrimSpace(requested)
	switch strings.ToLower(trimmed) {
	case "gemma-3-1b-it", "gemma-3-1b-it-q4_k_m", "gemma-3-1b-it-q4_k_m.gguf":
		name := trimmed
		if name == "" {
			name = "gemma-3-1b-it"
		}
		return modelSelection{
			name: name,
			file: "gemma-3-1b-it-Q4_K_M.gguf",
			url:  "https://huggingface.co/ggml-org/gemma-3-1b-it-GGUF/resolve/main/gemma-3-1b-it-Q4_K_M.gguf?download=true",
		}
	case "gemma-3-4b-it", "gemma-3-4b-it-q4_k_m", "gemma-3-4b-it-q4_k_m.gguf":
		name := trimmed
		if name == "" {
			name = "gemma-3-4b-it"
		}
		return modelSelection{
			name: name,
			file: "gemma-3-4b-it-Q4_K_M.gguf",
			url:  "https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf?download=true",
		}
	case "gemma-3-12b-it", "gemma-3-12b-it-q4_k_m", "gemma-3-12b-it-q4_k_m.gguf":
		name := trimmed
		if name == "" {
			name = "gemma-3-12b-it"
		}
		return modelSelection{
			name: name,
			file: "gemma-3-12b-it-Q4_K_M.gguf",
			url:  "https://huggingface.co/ggml-org/gemma-3-12b-it-GGUF/resolve/main/gemma-3-12b-it-Q4_K_M.gguf?download=true",
		}
	case "phi-2":
		name := trimmed
		if name == "" {
			name = "phi-2"
		}
		return modelSelection{
			name: name,
			file: "phi-2.Q4_0.gguf",
			url:  "https://huggingface.co/TheBloke/phi-2-GGUF/resolve/main/phi-2.Q4_0.gguf?download=true",
		}
	case "tinyllama-1.1b-chat-v1.0", "tinyllama", "", "default":
		return modelSelection{
			name: trimmed,
			file: "tinyllama.gguf",
			url:  "https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf?download=true",
		}
	default:
		return modelSelection{
			name: trimmed,
			file: "tinyllama.gguf",
			url:  "https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf?download=true",
		}
	}
}

func (r *LLMInstanceReconciler) reconcileDeployment(ctx context.Context, inst *llmv1alpha1.LLMInstance, labels map[string]string, model modelSelection) error {
	logger := log.FromContext(ctx)
	deployName := fmt.Sprintf("%s-llama", inst.Name)
	desiredReplicas := inst.Spec.Replicas
	if desiredReplicas <= 0 {
		desiredReplicas = 1
	}

	var existing appsv1.Deployment
	key := client.ObjectKey{Namespace: inst.Namespace, Name: deployName}
	if err := r.Get(ctx, key, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		deploy := buildDeployment(inst, labels, desiredReplicas, model)
		if err := ctrl.SetControllerReference(inst, &deploy, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &deploy); err != nil {
			return err
		}
		logger.Info("created llama.cpp Deployment", "name", deployName)
		return nil
	}

	replicasChanged, previousReplicas := ensureDeploymentReplicas(&existing, desiredReplicas)
	modelChanged := ensureDeploymentModel(&existing, model)
	if !replicasChanged && !modelChanged {
		return nil
	}
	if err := r.Update(ctx, &existing); err != nil {
		return err
	}
	if replicasChanged {
		logger.Info("updated Deployment replicas", "name", deployName, "from", previousReplicas, "to", desiredReplicas)
	}
	if modelChanged {
		logger.Info("updated Deployment model configuration", "name", deployName, "model", model.name, "path", model.path())
	}
	return nil
}

func buildDeployment(inst *llmv1alpha1.LLMInstance, labels map[string]string, replicas int32, model modelSelection) appsv1.Deployment {
	modelPath := model.path()
	volume := corev1.Volume{
		Name: "models-volume",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	initContainer := corev1.Container{
		Name:    "download-model",
		Image:   "curlimages/curl:8.8.0",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{fmt.Sprintf("curl -fL -o %s %s", modelPath, model.url)},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "models-volume",
			MountPath: "/models",
		}},
	}
	container := corev1.Container{
		Name:    "llama-cpp-server",
		Image:   "ghcr.io/ggml-org/llama.cpp:server-b7045",
		Command: []string{"/app/llama-server"},
		Args: []string{
			"-m", modelPath,
			"--port", "8000",
			"--host", "0.0.0.0",
		},
		Env:            []corev1.EnvVar{{Name: "MODEL_PATH", Value: modelPath}},
		Ports:          []corev1.ContainerPort{{ContainerPort: 8000, Protocol: corev1.ProtocolTCP}},
		ReadinessProbe: llmReadinessProbe(),
		LivenessProbe:  llmLivenessProbe(),
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "models-volume",
			MountPath: "/models",
		}},
	}

	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-llama", inst.Name),
			Namespace: inst.Namespace,
			Labels:    copyStringMap(labels),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: copyStringMap(labels)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: copyStringMap(labels)},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{initContainer},
					Containers:     []corev1.Container{container},
					Volumes:        []corev1.Volume{volume},
				},
			},
		},
	}
}

func ensureDeploymentReplicas(deploy *appsv1.Deployment, desired int32) (bool, int32) {
	var current int32
	if deploy.Spec.Replicas != nil {
		current = *deploy.Spec.Replicas
	}
	if deploy.Spec.Replicas == nil || current != desired {
		deploy.Spec.Replicas = ptrTo(desired)
		return true, current
	}
	return false, current
}

func ensureDeploymentModel(deploy *appsv1.Deployment, model modelSelection) bool {
	updated := false
	modelPath := model.path()
	desiredInitArg := fmt.Sprintf("curl -fL -o %s %s", modelPath, model.url)
	for i := range deploy.Spec.Template.Spec.InitContainers {
		c := &deploy.Spec.Template.Spec.InitContainers[i]
		if c.Name != "download-model" {
			continue
		}
		if len(c.Args) != 1 || c.Args[0] != desiredInitArg {
			c.Args = []string{desiredInitArg}
			updated = true
		}
	}

	desiredArgs := []string{"-m", modelPath, "--port", "8000", "--host", "0.0.0.0"}
	for i := range deploy.Spec.Template.Spec.Containers {
		c := &deploy.Spec.Template.Spec.Containers[i]
		if c.Name != "llama-cpp-server" {
			continue
		}
		hasEnv := false
		for j := range c.Env {
			env := &c.Env[j]
			if env.Name != "MODEL_PATH" {
				continue
			}
			hasEnv = true
			if env.Value != modelPath {
				env.Value = modelPath
				updated = true
			}
		}
		if !hasEnv {
			c.Env = append(c.Env, corev1.EnvVar{Name: "MODEL_PATH", Value: modelPath})
			updated = true
		}
		if !stringSliceEqual(c.Args, desiredArgs) {
			c.Args = desiredArgs
			updated = true
		}
		if ensureLLMContainerProbes(c) {
			updated = true
		}
	}

	return updated
}

func llmReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(8000),
			},
		},
		PeriodSeconds:    5,
		TimeoutSeconds:   2,
		FailureThreshold: 6,
	}
}

func llmLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(8000),
			},
		},
		InitialDelaySeconds: 20,
		PeriodSeconds:       10,
		TimeoutSeconds:      2,
		FailureThreshold:    6,
	}
}

func ensureLLMContainerProbes(c *corev1.Container) bool {
	updated := false
	if c.ReadinessProbe == nil ||
		c.ReadinessProbe.HTTPGet == nil ||
		c.ReadinessProbe.HTTPGet.Path != "/health" ||
		c.ReadinessProbe.HTTPGet.Port.IntValue() != 8000 {
		c.ReadinessProbe = llmReadinessProbe()
		updated = true
	}
	if c.LivenessProbe == nil ||
		c.LivenessProbe.HTTPGet == nil ||
		c.LivenessProbe.HTTPGet.Path != "/health" ||
		c.LivenessProbe.HTTPGet.Port.IntValue() != 8000 {
		c.LivenessProbe = llmLivenessProbe()
		updated = true
	}
	return updated
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ptrTo[T any](v T) *T {
	return &v
}

func (r *LLMInstanceReconciler) reconcileService(ctx context.Context, inst *llmv1alpha1.LLMInstance, labels map[string]string, svcName string) error {
	logger := log.FromContext(ctx)
	var svc corev1.Service
	key := client.ObjectKey{Namespace: inst.Namespace, Name: svcName}
	if err := r.Get(ctx, key, &svc); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: inst.Namespace,
				Labels:    copyStringMap(labels),
			},
			Spec: corev1.ServiceSpec{
				Selector: copyStringMap(labels),
				Ports: []corev1.ServicePort{{
					Name:       "http",
					Port:       8000,
					TargetPort: intstr.FromInt(8000),
					Protocol:   corev1.ProtocolTCP,
				}},
			},
		}
		if err := ctrl.SetControllerReference(inst, &service, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &service); err != nil {
			return err
		}
		logger.Info("created llama.cpp Service", "name", svcName)
	}
	return nil
}

func (r *LLMInstanceReconciler) reconcileStripMiddleware(ctx context.Context, inst *llmv1alpha1.LLMInstance, labels map[string]string, svcName, pathPrefix string) error {
	logger := log.FromContext(ctx)
	mwName := stripMiddlewareName(svcName)
	mw := &unstructured.Unstructured{}
	mw.SetGroupVersionKind(schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"})
	key := client.ObjectKey{Namespace: inst.Namespace, Name: mwName}
	if err := r.Get(ctx, key, mw); err != nil {
		if meta.IsNoMatchError(err) || strings.Contains(err.Error(), "no matches for kind \"Middleware\"") {
			logger.Info("Traefik Middleware CRD not found; skipping middleware creation", "error", err.Error())
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		newMW := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "traefik.io/v1alpha1",
			"kind":       "Middleware",
			"metadata": map[string]interface{}{
				"name":      mwName,
				"namespace": inst.Namespace,
				"labels":    copyInterfaceMap(labels),
			},
			"spec": map[string]interface{}{
				"stripPrefix": map[string]interface{}{
					"prefixes": []interface{}{pathPrefix},
				},
			},
		}}
		if err := ctrl.SetControllerReference(inst, newMW, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, newMW); err != nil {
			return err
		}
		logger.Info("created Traefik Middleware for prefix strip", "name", mwName, "path", pathPrefix)
	}
	return nil
}

func (r *LLMInstanceReconciler) reconcileForwardAuthMiddleware(ctx context.Context, inst *llmv1alpha1.LLMInstance, labels map[string]string, svcName, slug string) error {
	logger := log.FromContext(ctx)
	mwName := authMiddlewareName(svcName)
	mw := &unstructured.Unstructured{}
	mw.SetGroupVersionKind(schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"})
	key := client.ObjectKey{Namespace: inst.Namespace, Name: mwName}
	if err := r.Get(ctx, key, mw); err != nil {
		if meta.IsNoMatchError(err) || strings.Contains(err.Error(), "no matches for kind \"Middleware\"") {
			logger.Info("Traefik Middleware CRD not found; skipping forwardAuth middleware creation", "error", err.Error())
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		authAddr := fmt.Sprintf("%s/auth/verify?slug=%s", r.AuthServiceURL, slug)
		newMW := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "traefik.io/v1alpha1",
			"kind":       "Middleware",
			"metadata": map[string]interface{}{
				"name":      mwName,
				"namespace": inst.Namespace,
				"labels":    copyInterfaceMap(labels),
			},
			"spec": map[string]interface{}{
				"forwardAuth": map[string]interface{}{
					"address":             authAddr,
					"trustForwardHeader":  true,
					"authResponseHeaders": []interface{}{},
				},
			},
		}}
		if err := ctrl.SetControllerReference(inst, newMW, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, newMW); err != nil {
			return err
		}
		logger.Info("created Traefik Middleware for forwardAuth", "name", mwName)
	}
	return nil
}

func (r *LLMInstanceReconciler) reconcileIngress(ctx context.Context, inst *llmv1alpha1.LLMInstance, labels map[string]string, svcName, pathPrefix string) error {
	logger := log.FromContext(ctx)
	ingressName := svcName
	var ing networkingv1.Ingress
	key := client.ObjectKey{Namespace: inst.Namespace, Name: ingressName}
	if err := r.Get(ctx, key, &ing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		newIngress := r.buildDesiredIngress(inst, labels, svcName, pathPrefix)
		if err := ctrl.SetControllerReference(inst, newIngress, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, newIngress); err != nil {
			return err
		}
		logger.Info("created Ingress for llama.cpp", "name", ingressName)
		return nil
	}

	isHTTPS := strings.EqualFold(r.PublicScheme, "https")
	desiredEntry := r.desiredIngressEntryPoints(isHTTPS)
	desiredMiddleware := desiredMiddlewareAnnotation(inst.Namespace, svcName)

	updated := r.ensureIngressAnnotations(&ing, desiredMiddleware, desiredEntry, isHTTPS)
	if r.applyExtraIngressAnnotations(&ing, true) {
		updated = true
	}
	if r.ensureIngressClass(&ing) {
		updated = true
	}
	if r.ensureIngressHTTPRule(&ing, pathPrefix, svcName) {
		updated = true
	}
	if r.ensureIngressTLS(&ing, isHTTPS) {
		updated = true
	}

	if !updated {
		return nil
	}
	if err := r.Update(ctx, &ing); err != nil {
		return err
	}
	logger.Info("updated Ingress to desired configuration", "ingress", ingressName)
	return nil
}

func (r *LLMInstanceReconciler) buildDesiredIngress(inst *llmv1alpha1.LLMInstance, labels map[string]string, svcName, pathPrefix string) *networkingv1.Ingress {
	className := "traefik"
	pathType := networkingv1.PathTypePrefix
	isHTTPS := strings.EqualFold(r.PublicScheme, "https")
	desiredEntry := r.desiredIngressEntryPoints(isHTTPS)
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: inst.Namespace,
			Labels:    copyStringMap(labels),
			Annotations: map[string]string{
				"traefik.ingress.kubernetes.io/router.entrypoints": desiredEntry,
				"traefik.ingress.kubernetes.io/router.middlewares": desiredMiddlewareAnnotation(inst.Namespace, svcName),
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			Rules: []networkingv1.IngressRule{{
				Host: r.PublicHost,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     pathPrefix,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: svcName,
									Port: networkingv1.ServiceBackendPort{Number: 8000},
								},
							},
						}},
					},
				},
			}},
			TLS: func() []networkingv1.IngressTLS {
				if !isHTTPS {
					return nil
				}
				desiredTLS := strings.TrimSpace(r.TLSSecretName)
				if desiredTLS == "" {
					return nil
				}
				return []networkingv1.IngressTLS{{
					Hosts:      []string{r.PublicHost},
					SecretName: desiredTLS,
				}}
			}(),
		},
	}
	r.applyExtraIngressAnnotations(ingress, false)
	return ingress
}

func (r *LLMInstanceReconciler) desiredIngressEntryPoints(isHTTPS bool) string {
	if isHTTPS {
		return "websecure,web"
	}
	return "web"
}

func (r *LLMInstanceReconciler) ensureIngressAnnotations(ing *networkingv1.Ingress, desiredMiddleware, desiredEntry string, isHTTPS bool) bool {
	updated := false
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	if ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] != desiredMiddleware {
		ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = desiredMiddleware
		updated = true
	}
	if ing.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"] != desiredEntry {
		ing.Annotations["traefik.ingress.kubernetes.io/router.entrypoints"] = desiredEntry
		updated = true
	}
	if isHTTPS {
		if ing.Annotations["traefik.ingress.kubernetes.io/router.tls"] != "true" {
			ing.Annotations["traefik.ingress.kubernetes.io/router.tls"] = "true"
			updated = true
		}
	} else if _, exists := ing.Annotations["traefik.ingress.kubernetes.io/router.tls"]; exists {
		delete(ing.Annotations, "traefik.ingress.kubernetes.io/router.tls")
		updated = true
	}
	return updated
}

func (r *LLMInstanceReconciler) applyExtraIngressAnnotations(ing *networkingv1.Ingress, override bool) bool {
	if len(r.ExtraIngressAnnotations) == 0 {
		return false
	}
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	updated := false
	for k, v := range r.ExtraIngressAnnotations {
		current, exists := ing.Annotations[k]
		if !exists || override {
			ing.Annotations[k] = v
			updated = true
			continue
		}
		if current != v {
			ing.Annotations[k] = v
			updated = true
		}
	}
	return updated
}

func (r *LLMInstanceReconciler) ensureIngressClass(ing *networkingv1.Ingress) bool {
	className := "traefik"
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != className {
		ing.Spec.IngressClassName = &className
		return true
	}
	return false
}

func (r *LLMInstanceReconciler) ensureIngressHTTPRule(ing *networkingv1.Ingress, pathPrefix, svcName string) bool {
	updated := false
	pathType := networkingv1.PathTypePrefix
	if len(ing.Spec.Rules) == 0 {
		ing.Spec.Rules = []networkingv1.IngressRule{{}}
		updated = true
	}
	rule := &ing.Spec.Rules[0]
	if rule.Host != r.PublicHost {
		rule.Host = r.PublicHost
		updated = true
	}
	if rule.HTTP == nil {
		rule.HTTP = &networkingv1.HTTPIngressRuleValue{}
		updated = true
	}
	if len(rule.HTTP.Paths) == 0 {
		rule.HTTP.Paths = []networkingv1.HTTPIngressPath{{}}
		updated = true
	}
	path := &rule.HTTP.Paths[0]
	if path.PathType == nil {
		path.PathType = &pathType
		updated = true
	}
	if *path.PathType != pathType {
		path.PathType = &pathType
		updated = true
	}
	if path.Path != pathPrefix {
		path.Path = pathPrefix
		updated = true
	}
	if path.Backend.Service == nil {
		path.Backend.Service = &networkingv1.IngressServiceBackend{}
		updated = true
	}
	if path.Backend.Service.Name != svcName {
		path.Backend.Service.Name = svcName
		updated = true
	}
	if path.Backend.Service.Port.Number != 8000 {
		path.Backend.Service.Port = networkingv1.ServiceBackendPort{Number: 8000}
		updated = true
	}
	return updated
}

func (r *LLMInstanceReconciler) ensureIngressTLS(ing *networkingv1.Ingress, isHTTPS bool) bool {
	desiredTLS := strings.TrimSpace(r.TLSSecretName)
	if !isHTTPS || desiredTLS == "" {
		return false
	}
	if len(ing.Spec.TLS) == 0 || ing.Spec.TLS[0].SecretName != desiredTLS || len(ing.Spec.TLS[0].Hosts) == 0 || ing.Spec.TLS[0].Hosts[0] != r.PublicHost {
		ing.Spec.TLS = []networkingv1.IngressTLS{{
			Hosts:      []string{r.PublicHost},
			SecretName: desiredTLS,
		}}
		return true
	}
	return false
}

func desiredMiddlewareAnnotation(namespace, svcName string) string {
	return fmt.Sprintf("%s-%s@kubernetescrd,%s-%s@kubernetescrd", namespace, authMiddlewareName(svcName), namespace, stripMiddlewareName(svcName))
}

func stripMiddlewareName(svcName string) string {
	return fmt.Sprintf("%s-strip", svcName)
}

func authMiddlewareName(svcName string) string {
	return fmt.Sprintf("%s-auth", svcName)
}

func (r *LLMInstanceReconciler) evaluateInstanceReadiness(ctx context.Context, inst *llmv1alpha1.LLMInstance, svcName string) (bool, string, string, error) {
	deployName := fmt.Sprintf("%s-llama", inst.Name)
	var deploy appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: deployName}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "DeploymentNotFound", fmt.Sprintf("Deployment %q not found", deployName), nil
		}
		return false, "", "", err
	}

	desiredReplicas := int32(1)
	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
		desiredReplicas = *deploy.Spec.Replicas
	}

	if deploy.Status.ObservedGeneration < deploy.Generation {
		return false, "DeploymentProgressing", fmt.Sprintf("Deployment %q has not observed latest generation", deployName), nil
	}
	if deploy.Status.UpdatedReplicas < desiredReplicas {
		return false, "DeploymentProgressing", fmt.Sprintf("Deployment %q updated replicas %d/%d", deployName, deploy.Status.UpdatedReplicas, desiredReplicas), nil
	}
	if deploy.Status.ReadyReplicas < desiredReplicas {
		return false, "DeploymentNotReady", fmt.Sprintf("Deployment %q ready replicas %d/%d", deployName, deploy.Status.ReadyReplicas, desiredReplicas), nil
	}
	if deploy.Status.AvailableReplicas < desiredReplicas {
		return false, "DeploymentNotAvailable", fmt.Sprintf("Deployment %q available replicas %d/%d", deployName, deploy.Status.AvailableReplicas, desiredReplicas), nil
	}

	endpointsReady, message, err := r.serviceHasReadyEndpoints(ctx, inst.Namespace, svcName, 8000)
	if err != nil {
		return false, "", "", err
	}
	if !endpointsReady {
		return false, "ServiceEndpointsMissing", message, nil
	}

	probeURL := r.serviceProbeURL(inst.Namespace, svcName, 8000, "/health")
	if err := r.runServiceHealthCheck(ctx, probeURL); err != nil {
		return false, "HealthCheckFailed", fmt.Sprintf("Service probe failed for %s: %v", probeURL, err), nil
	}

	return true, "Provisioned", "LLM instance is ready", nil
}

func (r *LLMInstanceReconciler) serviceHasReadyEndpoints(ctx context.Context, namespace, serviceName string, expectedPort int32) (bool, string, error) {
	var endpoints corev1.Endpoints
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: serviceName}, &endpoints); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Endpoints for Service %q not found", serviceName), nil
		}
		return false, "", err
	}
	for _, subset := range endpoints.Subsets {
		if len(subset.Addresses) == 0 {
			continue
		}
		for _, port := range subset.Ports {
			if port.Port == expectedPort {
				return true, "", nil
			}
		}
	}
	return false, fmt.Sprintf("Service %q has no ready endpoints on port %d", serviceName, expectedPort), nil
}

func (r *LLMInstanceReconciler) serviceProbeURL(namespace, serviceName string, port int32, path string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", serviceName, namespace, port, path)
}

func (r *LLMInstanceReconciler) runServiceHealthCheck(ctx context.Context, targetURL string) error {
	checker := r.ServiceHealthChecker
	if checker == nil {
		checker = defaultServiceHealthChecker
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return checker(probeCtx, targetURL)
}

func defaultServiceHealthChecker(ctx context.Context, targetURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return nil
}

func (r *LLMInstanceReconciler) updateProvisioningStatus(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug, reason, message string) error {
	notReadyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: inst.Generation,
		Reason:             reason,
		Message:            message,
	}
	meta.SetStatusCondition(&inst.Status.Conditions, notReadyCond)
	inst.Status.Phase = "Provisioning"
	inst.Status.Endpoint = r.instanceEndpoint(slug)
	inst.Status.ObservedGeneration = inst.Generation
	return r.Status().Update(ctx, inst)
}

func (r *LLMInstanceReconciler) updateReadyStatus(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	readyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: inst.Generation,
		Reason:             "Provisioned",
		Message:            "LLM instance is ready",
	}

	meta.SetStatusCondition(&inst.Status.Conditions, readyCond)
	inst.Status.Phase = "Ready"
	inst.Status.Endpoint = r.instanceEndpoint(slug)
	inst.Status.ObservedGeneration = inst.Generation
	return r.Status().Update(ctx, inst)
}

func (r *LLMInstanceReconciler) instanceEndpoint(slug string) string {
	scheme := r.PublicScheme
	if scheme == "" {
		scheme = "http"
	}
	if strings.TrimSpace(r.PublicHost) == "" || strings.TrimSpace(slug) == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s/llm/%s", scheme, r.PublicHost, slug)
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyInterfaceMap(in map[string]string) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLMInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmv1alpha1.LLMInstance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Complete(r)
}

// generateSlug creates a short, label-safe, URL-safe slug from cryptographically
// random bytes. It uses unpadded base64url, truncated, and ensures the first and
// last characters are alphanumeric to satisfy Kubernetes label value rules.
func generateSlug(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	// Cap to max 16 chars to keep URLs compact
	if len(s) > 16 {
		s = s[:16]
	}
	// Ensure it starts and ends with an alphanumeric character (Kubernetes label requirement)
	if len(s) > 0 {
		sb := []byte(s)
		if !isAlphaNumeric(sb[0]) {
			sb[0] = 'a'
		}
		if !isAlphaNumeric(sb[len(sb)-1]) {
			sb[len(sb)-1] = 'z'
		}
		s = string(sb)
	}
	return s, nil
}

func isAlphaNumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
