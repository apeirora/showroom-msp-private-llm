package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSlugRejectsUnconfiguredTenantAuthority(t *testing.T) {
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{
		Name: "attacker", Namespace: "tenant-b", UID: "attacker-uid", CreationTimestamp: metav1.NewTime(time.Now()),
		Annotations: map[string]string{slugAnnotationKey: "victim-slug"},
	}}
	r := &LLMInstanceReconciler{}
	if _, _, err := r.ensureSlug(context.Background(), inst); err == nil {
		t.Fatal("tenant annotation was accepted without an operator policy")
	}
}

func TestSlugDoesNotAdoptHostedBackends(t *testing.T) {
	for _, kind := range []string{"Service", "Deployment"} {
		for _, owner := range []string{"missing", "replacement", "different-kind", "valid"} {
			t.Run(kind+"/"+owner, func(t *testing.T) {
				r, inst := slugOwnershipFixture(t)
				deployment := buildDeployment(inst, llamaLabels(inst.Name), 1, resolveModel(tinyLlamaModel))
				var object client.Object = &deployment
				if kind == "Service" {
					object = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: inst.Name + "-llama", Namespace: inst.Namespace}}
				}
				if owner != "missing" {
					if err := ctrl.SetControllerReference(inst, object, r.Scheme); err != nil {
						t.Fatal(err)
					}
					refs := object.GetOwnerReferences()
					if owner == "replacement" {
						refs[0].UID = "previous-instance"
					}
					if owner == "different-kind" {
						refs[0].Kind = "DifferentKind"
					}
					object.SetOwnerReferences(refs)
				}
				if err := r.Create(context.Background(), object); err != nil {
					t.Fatal(err)
				}
				before := object.DeepCopyObject()
				var err error
				if kind == "Service" {
					err = r.reconcileService(context.Background(), inst, llamaLabels(inst.Name), inst.Name+"-llama")
				} else {
					err = r.reconcileDeployment(context.Background(), inst, llamaLabels(inst.Name), resolveModel("phi-2"))
				}
				if owner == "valid" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if !errors.Is(err, errSlugBackendOwnership) {
					t.Fatalf("foreign backend was accepted: %v", err)
				}
				if err := r.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, object) {
					t.Fatal("foreign backend was changed")
				}
			})
		}
	}
}

func TestSlugForeignBackendWithdrawsOnlyOwnedIngress(t *testing.T) {
	ctx := context.Background()
	r, inst := slugOwnershipFixture(t)
	inst.Finalizers = []string{"llm.privatellms.msp/llminstance-finalizer"}
	if err := r.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	inst.Status.Slug, inst.Status.SlugSource = testKeptSlug, testLegacySlugSource
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	deployment := buildDeployment(inst, llamaLabels(inst.Name), 1, resolveModel(tinyLlamaModel))
	ingress := r.buildDesiredIngress(inst, nil, inst.Name+"-llama", "/llm/kept-slug")
	for _, object := range []client.Object{&deployment, ingress} {
		if err := ctrl.SetControllerReference(inst, object, r.Scheme); err != nil {
			t.Fatal(err)
		}
		if err := r.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	foreign := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: inst.Name + "-llama", Namespace: inst.Namespace}}
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	before := foreign.DeepCopy()
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
	if !errors.Is(err, errSlugBackendOwnership) {
		t.Fatalf("unexpected backend rejection: %v", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(ingress), ingress); !apierrors.IsNotFound(err) {
		t.Fatal("owned route still exposed a foreign backend")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(foreign), foreign); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, foreign) {
		t.Fatal("foreign Service changed during owned-route withdrawal")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	if inst.Status.Endpoint != "" {
		t.Fatal("rejected backend remained advertised")
	}
}

func slugOwnershipFixture(t *testing.T) (*LLMInstanceReconciler, *llmv1alpha1.LLMInstance) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, llmv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "tenant-a", UID: "instance-a",
		Annotations: map[string]string{slugAnnotationKey: testKeptSlug}}}
	policy := testLegacySlugPolicy(inst)
	inst.Status.Endpoint = "https://inference.example.test/llm/kept-slug"
	inst.Status.Phase = slugReadyPhase
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(inst).WithObjects(inst).Build()
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	return &LLMInstanceReconciler{Client: c, Scheme: scheme, SlugPolicy: policy, PublicHost: "inference.example.test"}, inst
}

func TestSlugLegacyEndpointPreservedDespiteTenantOverride(t *testing.T) {
	r, inst := slugOwnershipFixture(t)
	endpoint := inst.Status.Endpoint
	inst.Annotations[slugAnnotationKey] = "victim-slug"
	if err := r.Update(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	slug, requeue, err := r.ensureSlug(context.Background(), inst)
	if err != nil || !requeue || slug != testKeptSlug {
		t.Fatalf("legacy migration failed: %s %v %v", slug, requeue, err)
	}
	var stored llmv1alpha1.LLMInstance
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(inst), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Endpoint != endpoint || stored.Status.Phase != slugReadyPhase || stored.Status.Slug != testKeptSlug || stored.Annotations[slugAnnotationKey] != "victim-slug" {
		t.Fatal("migration changed the approved endpoint or treated annotation repair as authority")
	}
	if _, again, err := r.ensureSlug(context.Background(), &stored); err != nil || again {
		t.Fatal("stable migration is not idempotent")
	}
}

type changedSlugReadback struct {
	client.Reader
	replaceIdentity bool
}

func (r changedSlugReadback) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if inst, ok := obj.(*llmv1alpha1.LLMInstance); ok {
		if r.replaceIdentity {
			inst.UID = "replacement"
		} else {
			inst.Status.Slug = ""
			inst.Status.SlugSource = ""
		}
	}
	return nil
}

func TestSlugPersistenceRejectsPrunedSchemaAndReplacementUID(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		r, inst := slugOwnershipFixture(t)
		r.RouteReader = changedSlugReadback{Reader: r.Client, replaceIdentity: replacement}
		_, _, err := r.ensureSlug(context.Background(), inst)
		want := "schema must persist"
		if replacement {
			want = "identity changed"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("untrusted persistence readback result: %v", err)
		}
	}
}

func TestSlugForeignRoutesAndFallbackBoundary(t *testing.T) {
	for _, scenario := range []string{"foreign-named", "exact", "descendant", "raw-prefix", "parent", "default-backend", "other-host", "other-path"} {
		t.Run(scenario, func(t *testing.T) {
			r, inst := slugOwnershipFixture(t)
			foreign := r.buildDesiredIngress(inst, nil, "other-service", "/llm/kept-slug")
			foreign.Name = "other-route"
			foreign.Namespace = "tenant-b"
			switch scenario {
			case "foreign-named":
				foreign.Name = inst.Name + "-llama"
				foreign.Namespace = inst.Namespace
			case "descendant":
				foreign.Spec.Rules[0].HTTP.Paths[0].Path = "/llm/kept-slug/v1/chat/completions"
			case "raw-prefix":
				foreign.Spec.Rules[0].HTTP.Paths[0].Path = "/llm/kept-slug-shadow"
			case "parent":
				foreign.Spec.Rules[0].HTTP.Paths[0].Path = "/"
			case "default-backend":
				foreign.Spec.DefaultBackend = &foreign.Spec.Rules[0].HTTP.Paths[0].Backend
				foreign.Spec.Rules = nil
			case "other-host":
				foreign.Spec.Rules[0].Host = "unrelated.example.test"
			case "other-path":
				foreign.Spec.Rules[0].HTTP.Paths = r.slugIngressRules("/portal", "other-service")[0].HTTP.Paths
			}
			if err := r.Create(context.Background(), foreign); err != nil {
				t.Fatal(err)
			}
			before := foreign.DeepCopy()
			err := r.checkSlugRoute(context.Background(), inst, testKeptSlug)
			allowed := scenario == "other-host" || scenario == "other-path"
			if (err == nil) != allowed {
				t.Fatalf("unexpected conflict result: %v", err)
			}
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(foreign), foreign); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, foreign) {
				t.Fatal("route check mutated a foreign object")
			}
		})
	}
}

func TestSlugIngressConvergesOwnedRouteWithoutAdoptingForeignRoute(t *testing.T) {
	for _, owned := range []bool{false, true} {
		r, inst := slugOwnershipFixture(t)
		ing := r.buildDesiredIngress(inst, nil, inst.Name+"-llama", "/llm/old")
		ing.Spec.Rules = append(ing.Spec.Rules, ing.Spec.Rules[0])
		ing.Spec.DefaultBackend = &ing.Spec.Rules[0].HTTP.Paths[0].Backend
		ing.Annotations["traefik.ingress.kubernetes.io/router.pathmatcher"] = "PathPrefix"
		if owned {
			if err := ctrl.SetControllerReference(inst, ing, r.Scheme); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Create(context.Background(), ing); err != nil {
			t.Fatal(err)
		}
		before := ing.DeepCopy()
		err := r.reconcileIngress(context.Background(), inst, nil, inst.Name+"-llama", "/llm/kept-slug")
		if (err == nil) != owned {
			t.Fatalf("route ownership result: %v", err)
		}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(ing), ing); err != nil {
			t.Fatal(err)
		}
		if !owned {
			if !reflect.DeepEqual(before, ing) {
				t.Fatal("foreign route mutated")
			}
			continue
		}
		if len(ing.Spec.Rules) != 1 || ing.Spec.DefaultBackend != nil || ing.Annotations["traefik.ingress.kubernetes.io/router.pathmatcher"] != "" ||
			!reflect.DeepEqual(ing.Spec.Rules, r.slugIngressRules("/llm/kept-slug", inst.Name+"-llama")) {
			t.Fatal("owned route retained stale or prefix-shadowing rules")
		}
	}
}

func TestSlugLegacyBYOCBindingAndDeletionRemainOnOriginalWorkload(t *testing.T) {
	ctx := context.Background()
	f := newBYOCCacheFixture(t, true)
	f.r.SlugPolicy = testLegacySlugPolicy(f.inst)
	if err := f.local.Update(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	f.inst.Annotations[slugAnnotationKey] = "attacker-override"
	if err := f.local.Update(ctx, f.inst); err != nil {
		t.Fatal(err)
	}
	if slug, _, err := f.r.ensureSlug(ctx, f.inst); err != nil || slug != cacheTestSlug {
		t.Fatalf("legacy workload identity changed: %v", err)
	}
	f.assertOldCache(t)
	// Mapping drift must not redirect cache or remote workload reconciliation.
	f.r.SlugPolicy = testLegacySlugPolicy(f.inst)
	if _, _, err := f.r.ensureSlug(ctx, f.inst); err == nil {
		t.Fatal("changed mapping bypassed immutable active BYOC identity")
	}
	f.assertOldCache(t)
	// A deleted legacy instance uses recorded active coordinates, even when
	// policy is absent and tenant metadata now names a different workload.
	f.r.SlugPolicy = nil
	f.inst.Status.ActiveBYOCTargetID = "" // Legacy workload predating the TLS target field.
	old := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: byocBaseName(cacheTestSlug), Namespace: byocNamespace,
		Annotations: map[string]string{byocOwnerUIDAnnotation: string(f.inst.UID)}}}
	foreign := old.DeepCopy()
	foreign.Name = byocBaseName("attacker-override")
	foreign.Annotations[byocOwnerUIDAnnotation] = "other-owner"
	for _, obj := range []client.Object{old, foreign} {
		if err := f.remote.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		err := f.r.cleanupBYOC(ctx, f.inst)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "not ready") {
			t.Fatal(err)
		}
	}
	if err := f.remote.Get(ctx, client.ObjectKeyFromObject(old), &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatal("old owned workload was orphaned")
	}
	if err := f.remote.Get(ctx, client.ObjectKeyFromObject(foreign), &appsv1.Deployment{}); err != nil {
		t.Fatal("annotation-selected foreign workload was touched")
	}
	if err := f.local.Get(ctx, f.local.cacheKey, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatal("protected cache was not retired after owned workload cleanup")
	}
}

func TestSlugRouteUsesSegmentBoundary(t *testing.T) {
	r := &LLMInstanceReconciler{PublicHost: "inference.example.test"}
	inst := &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "tenant-a"}}
	ingress := r.buildDesiredIngress(inst, nil, "model-llama", "/llm/victim-slug")
	paths := ingress.Spec.Rules[0].HTTP.Paths
	if len(paths) != 2 || paths[0].Path != "/llm/victim-slug" || *paths[0].PathType != networkingv1.PathTypeExact ||
		paths[1].Path != "/llm/victim-slug/" || *paths[1].PathType != networkingv1.PathTypePrefix {
		t.Fatal("route does not separate the exact slug from its slash-delimited descendants")
	}
}
