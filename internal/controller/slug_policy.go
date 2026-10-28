package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	"github.com/apeirora/showroom-msp-private-llm/internal/slugpolicy"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errSlugRouteOwnership   = errors.New("named Ingress is not controlled by this LLMInstance")
	errSlugRouteConflict    = errors.New("another Ingress overlaps the selected inference path")
	errSlugBackendOwnership = errors.New("named hosted backend is not controlled by this LLMInstance")
)

func (r *LLMInstanceReconciler) routeReader() client.Reader {
	if r.RouteReader != nil {
		return r.RouteReader
	}
	return r.Client
}

func (r *LLMInstanceReconciler) selectSlug(inst *llmv1alpha1.LLMInstance) (slugpolicy.Selection, error) {
	selection, err := r.SlugPolicy.Select(inst)
	if err != nil {
		return selection, err
	}
	if selection.Source == slugpolicy.Derived && inst.Status.Slug == "" &&
		(inst.Status.Endpoint != "" || inst.Status.ActiveBYOCSlug != "" || inst.Status.ActiveBYOCKubeconfigSecretName != "") {
		return slugpolicy.Selection{}, errors.New("a previously provisioned instance requires its reviewed legacy slug")
	}
	if (inst.Status.ActiveBYOCSlug != "" && inst.Status.ActiveBYOCSlug != selection.Slug) ||
		(inst.Status.Slug != "" && inst.Status.Slug != selection.Slug) {
		return slugpolicy.Selection{}, errors.New("operator slug mapping changed; explicit endpoint and workload migration is required")
	}
	return selection, nil
}

func (r *LLMInstanceReconciler) ensureSlug(ctx context.Context, inst *llmv1alpha1.LLMInstance) (string, bool, error) {
	selection, err := r.selectSlug(inst)
	if err != nil {
		return "", false, err
	}
	if inst.Status.Slug == selection.Slug && inst.Status.SlugSource == selection.Source {
		return selection.Slug, false, nil
	}
	before := inst.DeepCopy()
	identity, uid := client.ObjectKeyFromObject(inst), inst.UID
	inst.Status.Slug, inst.Status.SlugSource = selection.Slug, selection.Source
	if err := r.Status().Patch(ctx, inst, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return "", false, err
	}
	var persisted llmv1alpha1.LLMInstance
	if err := r.routeReader().Get(ctx, identity, &persisted); err != nil {
		return "", false, err
	}
	if persisted.UID != uid || client.ObjectKeyFromObject(&persisted) != identity {
		return "", false, errors.New("instance identity changed during slug persistence")
	}
	if persisted.Status.Slug != selection.Slug || persisted.Status.SlugSource != selection.Source {
		return "", false, errors.New("LLMInstance schema must persist status.slug and status.slugSource")
	}
	return selection.Slug, true, nil
}

func ownsSlugIngress(inst *llmv1alpha1.LLMInstance, ing *networkingv1.Ingress) bool {
	return ownsLLMResource(inst, ing)
}

func ownsLLMResource(inst *llmv1alpha1.LLMInstance, obj client.Object) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil && inst.UID != "" && obj.GetNamespace() == inst.Namespace && owner.UID == inst.UID &&
		owner.Name == inst.Name && owner.Kind == "LLMInstance" && owner.APIVersion == llmv1alpha1.GroupVersion.String()
}

func (r *LLMInstanceReconciler) rejectSlug(ctx context.Context, inst *llmv1alpha1.LLMInstance, cause error) error {
	// Never modify another instance's route. BYOC remote cleanup stays bound to
	// its recorded active slug and protected credentials; no remote rename here.
	var ing networkingv1.Ingress
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-llama"}
	routeErr := r.routeReader().Get(ctx, key, &ing)
	if routeErr == nil {
		if !ownsSlugIngress(inst, &ing) {
			routeErr = errSlugRouteOwnership
		} else {
			routeErr = client.IgnoreNotFound(r.Delete(ctx, &ing, client.Preconditions{UID: ptrTo(ing.UID), ResourceVersion: ptrTo(ing.ResourceVersion)}))
		}
	} else {
		routeErr = client.IgnoreNotFound(routeErr)
	}
	statusErr := r.updateProvisioningStatus(ctx, inst, "", "SlugOwnershipError", "Inference identity requires a valid operator policy and an exclusively owned route")
	return errors.Join(cause, routeErr, statusErr)
}

func hostClaimsSlugHost(rule, host string) bool {
	rule, host = strings.ToLower(rule), strings.ToLower(host)
	if rule == "" || rule == host {
		return true
	}
	return strings.HasPrefix(rule, "*.") && strings.HasSuffix(host, rule[1:]) && strings.Count(host, ".") == strings.Count(rule, ".")
}

func (r *LLMInstanceReconciler) checkSlugRoute(ctx context.Context, inst *llmv1alpha1.LLMInstance, slug string) error {
	key := client.ObjectKey{Namespace: inst.Namespace, Name: inst.Name + "-llama"}
	var named networkingv1.Ingress
	if err := r.routeReader().Get(ctx, key, &named); err == nil {
		if !ownsSlugIngress(inst, &named) {
			return errSlugRouteOwnership
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	var ingresses networkingv1.IngressList
	if err := r.routeReader().List(ctx, &ingresses); err != nil {
		return err
	}
	selectedPath := "/llm/" + slug
	for i := range ingresses.Items {
		ing := &ingresses.Items[i]
		if client.ObjectKeyFromObject(ing) == key && ownsSlugIngress(inst, ing) {
			continue
		}
		if ing.Spec.DefaultBackend != nil {
			return errSlugRouteConflict
		}
		for _, rule := range ing.Spec.Rules {
			if !hostClaimsSlugHost(rule.Host, r.PublicHost) || rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				// Conservative across controller-specific matchers: raw prefix overlap
				// cannot silently redirect another tenant's credential-bearing request.
				candidate := strings.TrimRight(path.Path, "/")
				if strings.HasPrefix(candidate, selectedPath) || strings.HasPrefix(selectedPath, candidate) {
					return errSlugRouteConflict
				}
			}
		}
	}
	return nil
}

func (r *LLMInstanceReconciler) slugIngressRules(path, svcName string) []networkingv1.IngressRule {
	exact, prefix := networkingv1.PathTypeExact, networkingv1.PathTypePrefix
	backend := networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svcName, Port: networkingv1.ServiceBackendPort{Number: 8000}}}
	return []networkingv1.IngressRule{{Host: r.PublicHost, IngressRuleValue: networkingv1.IngressRuleValue{
		HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
			{Path: path, PathType: &exact, Backend: backend}, {Path: fmt.Sprintf("%s/", path), PathType: &prefix, Backend: backend},
		}},
	}}}
}
