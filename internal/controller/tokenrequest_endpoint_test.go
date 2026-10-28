package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	tokenFixtureTarget   = "fixture-target-a"
	tokenFixtureHTTPURL  = "http://192.0.2.1:443"
	tokenFixtureHTTPSURL = "https://instance-a.byoc.example.test"
	tokenFixtureKey      = "fixture-only-token"
)

func tokenEndpointFixture(t *testing.T) (*APITokenRequestReconciler, *llmv1alpha1.LLMInstance, *llmv1alpha1.APITokenRequest, *corev1.Secret) {
	t.Helper()
	sch := runtime.NewScheme()
	if err := llmv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	inst := &llmv1alpha1.LLMInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "tenant-a", UID: "instance-a", Generation: 1,
			Annotations: map[string]string{slugAnnotationKey: "model-slug"}},
		Spec: llmv1alpha1.LLMInstanceSpec{ClusterRef: &llmv1alpha1.ClusterRef{KubeconfigSecretName: "cluster"}},
	}
	setInstanceReadyStatus(inst, tokenFixtureHTTPSURL)
	inst.Status.ActiveBYOCTargetID = tokenFixtureTarget
	tr := &llmv1alpha1.APITokenRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "access", Namespace: inst.Namespace, UID: "request-a", Generation: 1,
			Finalizers: []string{apiTokenRequestFinalizer}},
		Spec: llmv1alpha1.APITokenRequestSpec{InstanceName: inst.Name},
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "access-token", Namespace: inst.Namespace,
			Annotations: map[string]string{credentialTargetAnnotation: tokenFixtureTarget, credentialInstanceAnnotation: string(inst.UID)},
			Labels:      map[string]string{"llm.privatellms.msp/apitokenrequest": tr.Name}},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{openAIAPIKeyKey: []byte(tokenFixtureKey), openAIAPIURLKey: []byte(tokenFixtureHTTPURL)},
	}
	if err := ctrl.SetControllerReference(tr, sec, sch); err != nil {
		t.Fatal(err)
	}
	c := clientfake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(inst, tr).WithObjects(inst, tr, sec).Build()
	return &APITokenRequestReconciler{Client: c, Scheme: sch}, inst, tr, sec
}

func reconcileTokenEndpoint(t *testing.T, r *APITokenRequestReconciler, tr *llmv1alpha1.APITokenRequest) error {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(tr)})
	return err
}

func TestBYOCTokenEndpointURLContract(t *testing.T) {
	for _, endpoint := range []string{"https://instance.byoc.example.test", "https://instance.byoc.example.test/", "https://instance.byoc.example.test:443"} {
		if !validBYOCTokenEndpoint(endpoint) {
			t.Errorf("rejected valid endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{
		"", "http://instance.byoc.example.test:443", "//instance.byoc.example.test", "https:instance.byoc.example.test",
		"https://user:pass@instance.byoc.example.test", "https://instance.byoc.example.test@attacker.test",
		"https://instance.byoc.example.test?redirect=http://attacker.test", "https://instance.byoc.example.test?",
		"https://instance.byoc.example.test#fragment", "https://instance.byoc.example.test/path", "https://instance.byoc.example.test/%2f",
		"https://192.0.2.1", "https://[::1]", "https://localhost", "https://unavailable.invalid", "https://instance.byoc.example.test:",
		"https://instance.byoc.example.test:8000", "https://instance.byoc.example.test:bad", "https://instance.byoc.example.test\\@attacker.test",
	} {
		if validBYOCTokenEndpoint(endpoint) {
			t.Errorf("accepted unsafe endpoint %q", endpoint)
		}
	}
}

func TestBYOCTokenEndpointUnavailableAndRecovery(t *testing.T) {
	for _, scenario := range []string{"not-ready", "empty-endpoint", "http-ready", "stale-status", "stale-condition", "missing-instance", "missing-slug", "byoc-to-hosted", "retarget-pending-hosted"} {
		t.Run(scenario, func(t *testing.T) {
			r, inst, tr, sec := tokenEndpointFixture(t)
			setTokenEndpointScenario(t, r, inst, scenario)
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			assertTokenEndpointUnavailable(t, r, tr, sec)
			if scenario != "missing-instance" {
				assertTokenEndpointRecovery(t, r, inst, tr, sec)
			}
		})
	}
}

func setTokenEndpointScenario(t *testing.T, r *APITokenRequestReconciler, inst *llmv1alpha1.LLMInstance, scenario string) {
	t.Helper()
	ctx := context.Background()
	switch scenario {
	case "not-ready":
		inst.Status.Conditions[0].Status = metav1.ConditionFalse
	case "empty-endpoint":
		inst.Status.Endpoint = ""
	case "http-ready":
		inst.Status.Endpoint = tokenFixtureHTTPURL
	case "stale-status":
		inst.Status.ObservedGeneration = 0
	case "stale-condition":
		inst.Status.Conditions[0].ObservedGeneration = 0
	case "byoc-to-hosted":
		inst.Spec.ClusterRef = nil
		if err := r.Update(ctx, inst); err != nil {
			t.Fatal(err)
		}
		inst.Status.ActiveBYOCKubeconfigSecretName = "previous-cluster"
		inst.Status.Endpoint = tokenFixtureHTTPURL
	case "retarget-pending-hosted":
		inst.Spec.ClusterRef = nil
		if err := r.Update(ctx, inst); err != nil {
			t.Fatal(err)
		}
		inst.Status.Conditions[0].Status = metav1.ConditionFalse
	case "missing-slug":
		delete(inst.Annotations, slugAnnotationKey)
		if err := r.Update(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "missing-instance" {
		if err := r.Delete(ctx, inst); err != nil {
			t.Fatal(err)
		}
	} else if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
}

func assertTokenEndpointUnavailable(t *testing.T, r *APITokenRequestReconciler, tr *llmv1alpha1.APITokenRequest, sec *corev1.Secret) {
	t.Helper()
	ctx := context.Background()
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIURLKey]) != unavailableAPIURL || sec.Annotations[endpointUnavailableAnnotation] != byocBootstrapTrue {
		t.Fatal("unavailable instance still advertises the previous endpoint")
	}
	if string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey {
		t.Fatal("endpoint withdrawal changed the model key")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	if tr.Status.SecretName != sec.Name || !meta.IsStatusConditionFalse(tr.Status.Conditions, "Ready") || tr.Annotations[tokenSecretVersionAnnotation] != sec.ResourceVersion {
		t.Fatal("request does not publish the unavailable Secret revision and Pending status")
	}
}

func assertTokenEndpointRecovery(t *testing.T, r *APITokenRequestReconciler, inst *llmv1alpha1.LLMInstance, tr *llmv1alpha1.APITokenRequest, sec *corev1.Secret) {
	t.Helper()
	ctx := context.Background()
	if err := r.Get(ctx, client.ObjectKeyFromObject(inst), inst); err != nil {
		t.Fatal(err)
	}
	if inst.Annotations == nil {
		inst.Annotations = map[string]string{}
	}
	inst.Annotations[slugAnnotationKey] = "model-slug"
	inst.Spec.ClusterRef = &llmv1alpha1.ClusterRef{KubeconfigSecretName: "cluster"}
	if err := r.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	setInstanceReadyStatus(inst, tokenFixtureHTTPSURL)
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIURLKey]) != inst.Status.Endpoint || sec.Annotations[endpointUnavailableAnnotation] != "" || string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey {
		t.Fatal("verified HTTPS recovery did not preserve the key and restore only the endpoint")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(tr.Status.Conditions, "Ready") {
		t.Fatal("recovered request not Ready")
	}
}

func TestBYOCUnavailableDoesNotMintCredentials(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Status.Endpoint = tokenFixtureHTTPURL
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); !apierrors.IsNotFound(err) {
		t.Fatal("minted credentials before verified HTTPS readiness")
	}
}

func TestTokenEndpointNeverMutatesForeignSecret(t *testing.T) {
	for _, invalidOwner := range []string{"none", "different-uid", "different-kind", "wrong-type"} {
		for _, ready := range []bool{false, true} {
			t.Run(invalidOwner+map[bool]string{false: "/unavailable", true: "/ready"}[ready], func(t *testing.T) {
				ctx := context.Background()
				r, inst, tr, sec := tokenEndpointFixture(t)
				if !ready {
					inst.Status.Conditions[0].Status = metav1.ConditionFalse
					if err := r.Status().Update(ctx, inst); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
					t.Fatal(err)
				}
				switch invalidOwner {
				case "none":
					sec.OwnerReferences = nil
				case "different-uid":
					sec.OwnerReferences[0].UID = "different-request"
				case "different-kind":
					sec.OwnerReferences[0].Kind = "LLMInstance"
				case "wrong-type":
					sec.Type = corev1.SecretTypeTLS
				}
				if err := r.Update(ctx, sec); err != nil {
					t.Fatal(err)
				}
				before := sec.DeepCopy()
				if err := reconcileTokenEndpoint(t, r, tr); err == nil {
					t.Fatal("accepted foreign or incorrectly typed Secret")
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, sec) {
					t.Fatal("modified foreign Secret")
				}
			})
		}
	}
}

type failTokenRequestUpdate struct {
	client.Client
	failures int
}

func (c *failTokenRequestUpdate) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if _, ok := obj.(*llmv1alpha1.APITokenRequest); ok && c.failures > 0 {
		c.failures--
		return errors.New("injected APITokenRequest update failure")
	}
	return c.Client.Update(ctx, obj, opts...)
}

func TestTokenEndpointSyncRetriesAfterSecretWrite(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "ready"}[ready], func(t *testing.T) {
			ctx := context.Background()
			r, inst, tr, sec := tokenEndpointFixture(t)
			if !ready {
				inst.Status.Conditions[0].Status = metav1.ConditionFalse
				if err := r.Status().Update(ctx, inst); err != nil {
					t.Fatal(err)
				}
			}
			r.Client = &failTokenRequestUpdate{Client: r.Client, failures: 1}
			if err := reconcileTokenEndpoint(t, r, tr); err == nil {
				t.Fatal("sync trigger failure was swallowed")
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			versionAfterWrite := sec.ResourceVersion
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
				t.Fatal(err)
			}
			if tr.Annotations[tokenSecretVersionAnnotation] != versionAfterWrite || tr.Annotations[secretUpdatedAtAnnotation] == "" {
				t.Fatal("request failed to retry sync for the already-written Secret")
			}
			stamp := tr.Annotations[secretUpdatedAtAnnotation]
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
				t.Fatal(err)
			}
			if tr.Annotations[secretUpdatedAtAnnotation] != stamp {
				t.Fatal("unchanged Secret causes unbounded sync touches")
			}
		})
	}
}

func TestTokenEndpointHostedBehaviorUnchanged(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Spec.ClusterRef = nil
	if err := r.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	setInstanceReadyStatus(inst, "http://hosted.example.test/llm/model-slug")
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIURLKey]) != inst.Status.Endpoint {
		t.Fatal("changed hosted endpoint contract")
	}
}

func TestTokenFinalizerPreservesForeignLabelMatches(t *testing.T) {
	ctx := context.Background()
	r, _, tr, sec := tokenEndpointFixture(t)
	foreign := sec.DeepCopy()
	foreign.Name = "customer-kubeconfig"
	foreign.OwnerReferences = nil
	foreign.ResourceVersion = ""
	if err := r.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	if _, err := r.finalizeTokenRequest(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(foreign), foreign); err != nil {
		t.Fatal("deleted a foreign Secret based only on copied labels", err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); !apierrors.IsNotFound(err) {
		t.Fatal("did not clean up the owned token Secret")
	}
}

func TestTokenEndpointSyncsExplicitKeyRotation(t *testing.T) {
	ctx := context.Background()
	r, _, tr, sec := tokenEndpointFixture(t)
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	previousVersion := tr.Annotations[tokenSecretVersionAnnotation]
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	sec.Data[openAIAPIKeyKey] = []byte("fixture-only-rotated-token")
	if err := r.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	rotatedVersion := sec.ResourceVersion
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	if tr.Annotations[tokenSecretVersionAnnotation] != rotatedVersion || previousVersion == rotatedVersion {
		t.Fatal("explicit key rotation was not included in sync notification")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIKeyKey]) != "fixture-only-rotated-token" {
		t.Fatal("controller overwrote the rotated key")
	}
}
