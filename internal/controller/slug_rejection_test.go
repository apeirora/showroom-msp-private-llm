package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func readySlugRouteFixture(t *testing.T) (*LLMInstanceReconciler, *llmv1alpha1.LLMInstance, *networkingv1.Ingress) {
	t.Helper()
	ctx := context.Background()
	r, inst := slugOwnershipFixture(t)
	r.AuthServiceURL = "http://auth.provider.svc:8080"
	r.PublicScheme = "https"
	inst.Finalizers = []string{"llm.privatellms.msp/llminstance-finalizer"}
	if err := r.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	inst.Status.Slug, inst.Status.SlugSource = testKeptSlug, testLegacySlugSource
	inst.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Provisioned"}}
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	labels, svcName := llamaLabels(inst.Name), inst.Name+"-llama"
	for _, reconcile := range []func() error{
		func() error { return r.reconcileDeployment(ctx, inst, labels, modelForInstance(inst)) },
		func() error { return r.reconcileService(ctx, inst, labels, svcName) },
		func() error { return r.reconcileStripMiddleware(ctx, inst, labels, svcName, "/llm/"+testKeptSlug) },
		func() error { return r.reconcileForwardAuthMiddleware(ctx, inst, labels, svcName, testKeptSlug) },
		func() error { return r.reconcileIngress(ctx, inst, labels, svcName, "/llm/"+testKeptSlug) },
	} {
		if err := reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	var ingress networkingv1.Ingress
	if err := r.Get(ctx, client.ObjectKey{Namespace: inst.Namespace, Name: svcName}, &ingress); err != nil {
		t.Fatal(err)
	}
	return r, inst, &ingress
}

const (
	slugGetFailure         = "get"
	slugUpdateFailure      = "update"
	slugListFailure        = "list"
	slugStatusPatchFailure = "status-patch"
)

type slugTransientCase struct {
	name, operation string
	object          client.Object
	readOccurrence  int
	mutate          func(client.Object)
}

func slugTransientCases() []slugTransientCase {
	middleware := func(name string) client.Object {
		object := &unstructured.Unstructured{}
		object.SetName(name)
		return object
	}
	auth := middleware(authMiddlewareName("model-llama"))
	strip := middleware(stripMiddlewareName("model-llama"))
	changeAuth := func(object client.Object) {
		unstructured.RemoveNestedField(object.(*unstructured.Unstructured).Object, "spec", "forwardAuth", "address")
	}
	return []slugTransientCase{
		{"auth-upgrade-get", slugGetFailure, auth, 1, nil},
		{"auth-upgrade-update", slugUpdateFailure, auth, 1, changeAuth},
		{"slug-status-patch", slugStatusPatchFailure, &llmv1alpha1.LLMInstance{}, 0, nil},
		{"slug-readback", slugGetFailure, &llmv1alpha1.LLMInstance{}, 2, nil},
		{"route-get", slugGetFailure, &networkingv1.Ingress{}, 1, nil},
		{"route-list", slugListFailure, nil, 1, nil},
		{"deployment-get", slugGetFailure, &appsv1.Deployment{}, 1, nil},
		{"deployment-update", slugUpdateFailure, &appsv1.Deployment{}, 1, func(object client.Object) { object.(*appsv1.Deployment).Spec.Replicas = ptrTo(int32(2)) }},
		{"service-get", slugGetFailure, &corev1.Service{}, 1, nil},
		{"strip-get", slugGetFailure, strip, 1, nil},
		{"strip-update", slugUpdateFailure, strip, 1, func(object client.Object) {
			unstructured.RemoveNestedField(object.(*unstructured.Unstructured).Object, "spec", "stripPrefix", "prefixes")
		}},
		{"auth-final-get", slugGetFailure, auth, 2, nil},
		{"auth-final-update", slugUpdateFailure, auth, 2, changeAuth},
		{"ingress-route-get", slugGetFailure, &networkingv1.Ingress{}, 2, nil},
		{"ingress-route-list", slugListFailure, nil, 2, nil},
		{"ingress-get", slugGetFailure, &networkingv1.Ingress{}, 3, nil},
		{"ingress-update", slugUpdateFailure, &networkingv1.Ingress{}, 3, func(object client.Object) { object.(*networkingv1.Ingress).Spec.Rules = nil }},
	}
}

type slugTransientProbe struct {
	scenario slugTransientCase
	injected error
	reads    int
	fired    bool
}

func (p *slugTransientProbe) matches(object client.Object, name string) bool {
	target := p.scenario.object
	return reflect.TypeOf(object) == reflect.TypeOf(target) && (target.GetName() == "" || target.GetName() == name)
}

func (p *slugTransientProbe) fail() error { p.fired = true; return p.injected }

func (p *slugTransientProbe) get(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	selected := p.scenario.object != nil && p.matches(object, key.Name)
	if selected {
		p.reads++
	}
	selected = selected && p.reads == p.scenario.readOccurrence
	if selected && p.scenario.operation == slugGetFailure {
		return p.fail()
	}
	if err := c.Get(ctx, key, object, opts...); err != nil {
		return err
	}
	// Simulate a stale read requiring a write while the stored route stays healthy.
	if selected && p.scenario.mutate != nil {
		p.scenario.mutate(object)
	}
	return nil
}

func (p *slugTransientProbe) list(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*networkingv1.IngressList); ok && p.scenario.operation == slugListFailure {
		p.reads++
		if p.reads == p.scenario.readOccurrence {
			return p.fail()
		}
	}
	return c.List(ctx, list, opts...)
}

func (p *slugTransientProbe) update(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
	if p.scenario.operation == slugUpdateFailure && p.matches(object, object.GetName()) {
		return p.fail()
	}
	return c.Update(ctx, object, opts...)
}

func (p *slugTransientProbe) patchStatus(ctx context.Context, c client.Client, resource string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if p.scenario.operation == slugStatusPatchFailure {
		return p.fail()
	}
	return c.SubResource(resource).Patch(ctx, object, patch, opts...)
}

func TestSlugTransientReconcileErrorsPreserveRouteAndReadyStatus(t *testing.T) {
	for _, scenario := range slugTransientCases() {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			r, inst, ingress := readySlugRouteFixture(t)
			if _, statusMigration := scenario.object.(*llmv1alpha1.LLMInstance); statusMigration {
				inst.Status.Slug, inst.Status.SlugSource = "", ""
				if err := r.Status().Update(ctx, inst); err != nil {
					t.Fatal(err)
				}
			}
			beforeStatus, beforeIngress := inst.Status.DeepCopy(), ingress.DeepCopy()
			base := r.Client
			injected := apierrors.NewTimeoutError("synthetic API timeout", 1)
			if scenario.operation == slugUpdateFailure || scenario.operation == slugStatusPatchFailure {
				injected = apierrors.NewConflict(schema.GroupResource{Resource: "synthetic"}, inst.Name, errors.New("synthetic concurrent update"))
			}
			probe := &slugTransientProbe{scenario: scenario, injected: injected}
			r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
				Get: probe.get, List: probe.list, Update: probe.update, SubResourcePatch: probe.patchStatus,
			})
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
			if !probe.fired || !errors.Is(err, injected) {
				t.Fatalf("expected stage-specific retry error (injected=%v): %v", probe.fired, err)
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(ingress), ingress); err != nil {
				t.Fatalf("transient failure removed healthy route: %v", err)
			}
			if !reflect.DeepEqual(beforeIngress, ingress) {
				t.Fatal("transient failure changed the existing route")
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status.Endpoint != beforeStatus.Endpoint || inst.Status.Phase != beforeStatus.Phase || !reflect.DeepEqual(inst.Status.Conditions, beforeStatus.Conditions) {
				t.Fatal("transient failure cleared or degraded the existing Ready status")
			}
		})
	}
}

const (
	slugMissingPolicy        = "missing-policy"
	slugUnmappedLegacy       = "unmapped-legacy"
	slugMappingDrift         = "mapping-drift"
	slugProvisionedDerived   = "previously-provisioned-derived"
	slugPrunedSchema         = "pruned-schema"
	slugReplacementReadback  = "replacement-readback"
	slugForeignDeployment    = "foreign-deployment"
	slugForeignService       = "foreign-service"
	slugForeignStrip         = "foreign-strip"
	slugForeignAuth          = "foreign-auth"
	slugForeignAuthLate      = "foreign-auth-late"
	slugMalformedStrip       = "malformed-strip"
	slugMalformedAuth        = "malformed-auth"
	slugMalformedAuthLate    = "malformed-auth-late"
	slugConflictingRoute     = "route-conflict"
	slugConflictingRouteLate = "route-conflict-late"
	slugForeignIngress       = "foreign-ingress"
	slugForeignIngressLate   = "foreign-ingress-late"
)

type slugRejectionProbe struct {
	stage                  string
	inst                   *llmv1alpha1.LLMInstance
	ingress                *networkingv1.Ingress
	protected              client.Object
	counts                 map[string]int
	lists                  int
	injected, foreignRoute bool
}

func (p *slugRejectionProbe) prepare(ctx context.Context, r *LLMInstanceReconciler) error {
	inst := p.inst
	switch p.stage {
	case slugMissingPolicy:
		r.SlugPolicy = nil
		p.injected = true
	case slugUnmappedLegacy:
		r.SlugPolicy = testLegacySlugPolicy()
		p.injected = true
	case slugMappingDrift:
		other := inst.DeepCopy()
		other.Annotations[slugAnnotationKey] = "changed-slug"
		r.SlugPolicy = testLegacySlugPolicy(other)
		p.injected = true
	case slugProvisionedDerived:
		r.SlugPolicy = testDerivedSlugPolicy()
		inst.Status.Slug, inst.Status.SlugSource = "", ""
		p.injected = true
	case slugPrunedSchema, slugReplacementReadback:
		inst.Status.Slug, inst.Status.SlugSource = "", ""
	case slugForeignDeployment:
		p.protected = &appsv1.Deployment{}
	case slugForeignService:
		p.protected = &corev1.Service{}
	case slugForeignIngress:
		p.protected, p.foreignRoute = &networkingv1.Ingress{}, true
	}
	if err := r.Status().Update(ctx, inst); err != nil {
		return err
	}
	if p.protected != nil {
		if err := r.Get(ctx, client.ObjectKeyFromObject(p.ingress), p.protected); err != nil {
			return err
		}
		p.protected.SetOwnerReferences(nil)
		if err := r.Update(ctx, p.protected); err != nil {
			return err
		}
		p.injected = true
	}
	return nil
}

func (p *slugRejectionProbe) corruptMiddleware(ctx context.Context, c client.Client, object *unstructured.Unstructured, hit int) error {
	strip := object.GetName() == stripMiddlewareName(p.inst.Name+"-llama")
	auth := object.GetName() == authMiddlewareName(p.inst.Name+"-llama")
	foreign := strip && p.stage == slugForeignStrip || auth && (p.stage == slugForeignAuth && hit == 1 || p.stage == slugForeignAuthLate && hit == 2)
	malformed := strip && p.stage == slugMalformedStrip || auth && (p.stage == slugMalformedAuth && hit == 1 || p.stage == slugMalformedAuthLate && hit == 2)
	if p.injected || (!foreign && !malformed) {
		return nil
	}
	if foreign {
		object.SetOwnerReferences(nil)
	}
	if malformed {
		// This is an actual stored shape error, not an API availability failure.
		object.Object["spec"] = "invalid middleware shape"
	}
	if err := c.Update(ctx, object); err != nil {
		return err
	}
	p.protected, p.injected = object.DeepCopy(), true
	return nil
}

func (p *slugRejectionProbe) get(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	counter := reflect.TypeOf(obj).String() + "/" + key.Name
	p.counts[counter]++
	hit := p.counts[counter]
	switch object := obj.(type) {
	case *llmv1alpha1.LLMInstance:
		if hit == 2 && p.stage == slugPrunedSchema {
			object.Status.Slug, object.Status.SlugSource = "", ""
			p.injected = true
		}
		if hit == 2 && p.stage == slugReplacementReadback {
			object.UID = "replacement-uid"
			p.injected = true
		}
	case *unstructured.Unstructured:
		return p.corruptMiddleware(ctx, c, object, hit)
	case *networkingv1.Ingress:
		if p.stage == slugForeignIngressLate && hit == 3 {
			object.SetOwnerReferences(nil)
			if err := c.Update(ctx, object); err != nil {
				return err
			}
			p.protected, p.injected, p.foreignRoute = object.DeepCopy(), true, true
		}
	}
	return nil
}

func (p *slugRejectionProbe) list(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
	if err := c.List(ctx, list, opts...); err != nil {
		return err
	}
	if routes, ok := list.(*networkingv1.IngressList); ok {
		p.lists++
		if p.stage == slugConflictingRoute && p.lists == 1 || p.stage == slugConflictingRouteLate && p.lists == 2 {
			foreign := p.ingress.DeepCopy()
			foreign.Name, foreign.Namespace = "competing", "other-tenant"
			foreign.OwnerReferences = nil
			routes.Items = append(routes.Items, *foreign)
			p.injected = true
		}
	}
	return nil
}

func TestSlugDefinitiveRejectionsWithdrawOnlyOwnedRoute(t *testing.T) {
	for _, stage := range []string{
		slugMissingPolicy, slugUnmappedLegacy, slugMappingDrift, slugProvisionedDerived,
		slugPrunedSchema, slugReplacementReadback, slugForeignDeployment, slugForeignService,
		slugForeignStrip, slugForeignAuth, slugForeignAuthLate, slugMalformedStrip, slugMalformedAuth, slugMalformedAuthLate,
		slugConflictingRoute, slugConflictingRouteLate, slugForeignIngress, slugForeignIngressLate,
	} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			r, inst, ingress := readySlugRouteFixture(t)
			base := r.Client
			probe := &slugRejectionProbe{stage: stage, inst: inst, ingress: ingress, counts: map[string]int{}}
			if err := probe.prepare(ctx, r); err != nil {
				t.Fatal(err)
			}
			r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Get: probe.get, List: probe.list})
			_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(inst)})
			if !probe.injected || !errors.Is(err, errSlugRejection) {
				t.Fatalf("unsafe route was not rejected (injected=%v): %v", probe.injected, err)
			}
			if !probe.foreignRoute {
				if err := base.Get(ctx, client.ObjectKeyFromObject(ingress), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
					t.Fatalf("unsafe owned route was not withdrawn: %v", err)
				}
			}
			if probe.protected != nil {
				actual := probe.protected.DeepCopyObject().(client.Object)
				if err := base.Get(ctx, client.ObjectKeyFromObject(probe.protected), actual); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(probe.protected, actual) {
					t.Fatal("rejection changed a foreign or malformed object")
				}
			}
			if err := base.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
				t.Fatal(err)
			}
			if inst.Status.Endpoint != "" || inst.Status.Phase == slugReadyPhase {
				t.Fatal("unsafe endpoint remained advertised as Ready")
			}
		})
	}
}
