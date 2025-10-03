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
}

const slugAnnotationKey = "llm.example.com/slug"

//+kubebuilder:rbac:groups=llm.example.com,resources=llminstances,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=llm.example.com,resources=llminstances/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=llm.example.com,resources=llminstances/finalizers,verbs=update
// core resources
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// apps and services
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
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

	if !inst.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
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

	pathPrefix := fmt.Sprintf("/llm/%s/%s", slug, inst.Name)

	if err := r.reconcileStripMiddleware(ctx, inst, labels, svcName, pathPrefix); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileForwardAuthMiddleware(ctx, inst, labels, svcName, slug); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileIngress(ctx, inst, labels, svcName, pathPrefix); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateInstanceStatus(ctx, inst, slug); err != nil {
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
		"app.kubernetes.io/name":   "llama-cpp-server",
		"llm.example.com/instance": instanceName,
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
	case "phi-2", "phi2":
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
		Image:   "ghcr.io/ggml-org/llama.cpp:server",
		Command: []string{"/app/llama-server"},
		Args: []string{
			"-m", modelPath,
			"--port", "8000",
			"--host", "0.0.0.0",
		},
		Env:   []corev1.EnvVar{{Name: "MODEL_PATH", Value: modelPath}},
		Ports: []corev1.ContainerPort{{ContainerPort: 8000, Protocol: corev1.ProtocolTCP}},
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
		className := "traefik"
		pathType := networkingv1.PathTypePrefix
		ing = networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ingressName,
				Namespace: inst.Namespace,
				Labels:    copyStringMap(labels),
				Annotations: map[string]string{
					"traefik.ingress.kubernetes.io/router.entrypoints": "web",
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
			},
		}
		if err := ctrl.SetControllerReference(inst, &ing, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &ing); err != nil {
			return err
		}
		logger.Info("created Ingress for llama.cpp", "name", ingressName)
		return nil
	}

	updated := false
	desiredAnnotation := desiredMiddlewareAnnotation(inst.Namespace, svcName)
	if ing.Annotations == nil {
		ing.Annotations = map[string]string{}
	}
	if ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] != desiredAnnotation {
		ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = desiredAnnotation
		updated = true
	}
	className := "traefik"
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != className {
		ing.Spec.IngressClassName = &className
		updated = true
	}
	if len(ing.Spec.Rules) > 0 && ing.Spec.Rules[0].HTTP != nil && len(ing.Spec.Rules[0].HTTP.Paths) > 0 {
		rule := &ing.Spec.Rules[0]
		if rule.Host != r.PublicHost {
			rule.Host = r.PublicHost
			updated = true
		}
		path := &rule.HTTP.Paths[0]
		if path.Path != pathPrefix {
			path.Path = pathPrefix
			updated = true
		}
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

func desiredMiddlewareAnnotation(namespace, svcName string) string {
	return fmt.Sprintf("%s-%s@kubernetescrd,%s-%s@kubernetescrd", namespace, authMiddlewareName(svcName), namespace, stripMiddlewareName(svcName))
}

func stripMiddlewareName(svcName string) string {
	return fmt.Sprintf("%s-strip", svcName)
}

func authMiddlewareName(svcName string) string {
	return fmt.Sprintf("%s-auth", svcName)
}

func (r *LLMInstanceReconciler) updateInstanceStatus(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	readyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Reason:             "Provisioned",
		Message:            "LLM instance is ready",
	}

	meta.SetStatusCondition(&inst.Status.Conditions, readyCond)
	inst.Status.Phase = "Ready"
	inst.Status.Endpoint = fmt.Sprintf("http://%s/llm/%s/%s", r.PublicHost, slug, inst.Name)
	inst.Status.ObservedGeneration = inst.Generation
	return r.Status().Update(ctx, inst)
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

// generateSlug creates a short, URL-safe opaque slug from cryptographically
// random bytes. The resulting string is unpadded base64url and truncated to
// a reasonable length for readability.
func generateSlug(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
	// Cap to max 16 chars to keep URLs compact
	if len(s) > 16 {
		s = s[:16]
	}
	return s, nil
}
