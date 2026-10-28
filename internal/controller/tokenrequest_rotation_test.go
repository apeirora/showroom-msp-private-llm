package controller

import (
	"bytes"
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const tokenReplacementTarget = "fixture-target-b"

func TestTokenCredentialTargetRotation(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		pending, legacy, move, hosted, unknown, rotate bool
	}{
		{name: "legacy migration ready", legacy: true, rotate: true},
		{name: "legacy migration pending", legacy: true, pending: true, rotate: true},
		{name: "target move ready", move: true, rotate: true},
		{name: "target move pending", move: true, pending: true, rotate: true},
		{name: "unknown target pending", pending: true, unknown: true},
		{name: "unknown target cannot stay ready", unknown: true},
		{name: "ordinary renewal pending", pending: true},
		{name: "ordinary renewal ready"},
		{name: "hosted replacement pending", hosted: true, pending: true},
		{name: "hosted replacement ready", hosted: true, rotate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, inst, tr, sec := tokenEndpointFixture(t)
			if tc.legacy {
				if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
					t.Fatal(err)
				}
				delete(sec.Annotations, credentialTargetAnnotation)
				if err := r.Update(ctx, sec); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hosted {
				inst.Spec.ClusterRef = nil
				if err := r.Update(ctx, inst); err != nil {
					t.Fatal(err)
				}
				inst.Status.ActiveBYOCTargetID = ""
			}
			if tc.move {
				inst.Status.ActiveBYOCTargetID = tokenReplacementTarget
			}
			if tc.unknown {
				inst.Status.ActiveBYOCTargetID = ""
			}
			if tc.pending {
				inst.Status.Conditions[0].Status = metav1.ConditionFalse
			}
			if err := r.Status().Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			keyChanged := string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey
			if keyChanged != tc.rotate {
				t.Fatalf("rotation=%v, expected %v", keyChanged, tc.rotate)
			}
			if tc.rotate && len(sec.Data[openAIAPIKeyKey]) < 40 {
				t.Fatal("replacement key is missing or unexpectedly short")
			}
			if tc.pending || tc.unknown {
				if string(sec.Data[openAIAPIURLKey]) != unavailableAPIURL {
					t.Fatal("pending key change retained a usable endpoint")
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
					t.Fatal(err)
				}
				if !meta.IsStatusConditionFalse(tr.Status.Conditions, "Ready") {
					t.Fatal("request remained Ready without an established target")
				}
			}
			if tc.rotate && sec.Annotations[credentialTargetAnnotation] != inst.Status.ActiveBYOCTargetID {
				t.Fatal("replacement key is not bound to its new target")
			}
			firstKey := append([]byte(nil), sec.Data[openAIAPIKeyKey]...)
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstKey, sec.Data[openAIAPIKeyKey]) {
				t.Fatal("same target caused repeated key rotation")
			}
		})
	}
}

func TestTokenCredentialRotationRetriesSyncWithoutRerotation(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Status.ActiveBYOCTargetID = tokenReplacementTarget
	inst.Status.Conditions[0].Status = metav1.ConditionFalse
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	r.Client = &failTokenRequestUpdate{Client: r.Client, failures: 1}
	if err := reconcileTokenEndpoint(t, r, tr); err == nil {
		t.Fatal("sync trigger failure was swallowed")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	rotatedKey := append([]byte(nil), sec.Data[openAIAPIKeyKey]...)
	if string(rotatedKey) == tokenFixtureKey || sec.Annotations[credentialTargetAnnotation] != tokenReplacementTarget {
		t.Fatal("pending key was not rotated before sync notification")
	}
	rotatedVersion := sec.ResourceVersion
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rotatedKey, sec.Data[openAIAPIKeyKey]) || sec.ResourceVersion != rotatedVersion {
		t.Fatal("retry rotated or rewrote the key again")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	if tr.Annotations[tokenSecretVersionAnnotation] != rotatedVersion {
		t.Fatal("rotated Secret revision was not retried for synchronization")
	}
}

func TestNewBYOCTokenBindsEstablishedTarget(t *testing.T) {
	ctx := context.Background()
	r, _, tr, sec := tokenEndpointFixture(t)
	if err := r.Delete(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if sec.Annotations[credentialTargetAnnotation] != tokenFixtureTarget {
		t.Fatal("new BYOC token lacks target identity")
	}
	// fake.Client retains StringData, while an API server converts it to Data.
	if len(sec.StringData[openAIAPIKeyKey]) < 40 {
		t.Fatal("new token key is missing or unexpectedly short")
	}
}

func TestTokenPendingRetargetRepairsInstanceLabel(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Status.ActiveBYOCTargetID = tokenReplacementTarget
	inst.Status.Conditions[0].Status = metav1.ConditionFalse
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	sec.Labels[instanceLabelKey] = "previous-instance"
	if err := r.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if sec.Labels[instanceLabelKey] != inst.Name || sec.Annotations[credentialTargetAnnotation] != tokenReplacementTarget {
		t.Fatal("new target cannot select the rotated Pending key")
	}
}

func TestTokenRotationNeverMutatesForeignSecret(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Status.ActiveBYOCTargetID = tokenReplacementTarget
	inst.Status.Conditions[0].Status = metav1.ConditionFalse
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	sec.OwnerReferences = nil
	if err := r.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err == nil {
		t.Fatal("accepted foreign Secret for rotation")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey || sec.Annotations[credentialTargetAnnotation] != tokenFixtureTarget {
		t.Fatal("rotated foreign Secret")
	}
}

func TestTokenRetargetWithinSameClusterRotatesInstanceKey(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	replacement := inst.DeepCopy()
	replacement.Name, replacement.UID, replacement.ResourceVersion = "other-model", "instance-b", ""
	if err := r.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
		t.Fatal(err)
	}
	tr.Spec.InstanceName = replacement.Name
	if err := r.Update(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIKeyKey]) == tokenFixtureKey {
		t.Fatal("retarget to another instance on the same cluster reused the old key")
	}
	if sec.Annotations[credentialInstanceAnnotation] != string(replacement.UID) || sec.Annotations[credentialTargetAnnotation] != tokenFixtureTarget {
		t.Fatal("replacement key lost its instance or cluster binding")
	}
}

func TestOrdinaryHostedTokenDoesNotRotate(t *testing.T) {
	ctx := context.Background()
	r, inst, tr, sec := tokenEndpointFixture(t)
	inst.Spec.ClusterRef = nil
	if err := r.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	inst.Status.ActiveBYOCTargetID = ""
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	delete(sec.Annotations, credentialTargetAnnotation)
	delete(sec.Annotations, credentialInstanceAnnotation)
	sec.Labels[instanceLabelKey] = inst.Name
	sec.Labels[slugAnnotationKey] = inst.Annotations[slugAnnotationKey]
	if err := r.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTokenEndpoint(t, r, tr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey {
		t.Fatal("ordinary hosted token was needlessly rotated")
	}
}

func TestHostedInstanceRetargetRotatesKey(t *testing.T) {
	for _, tc := range []struct {
		name            string
		legacy, pending bool
	}{
		{name: "known UID"}, {name: "legacy without annotations", legacy: true},
		{name: "known UID via Pending", pending: true}, {name: "legacy via Pending", legacy: true, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, inst, tr, sec := tokenEndpointFixture(t)
			inst.Spec.ClusterRef = nil
			if err := r.Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			inst.Status.ActiveBYOCTargetID = ""
			if err := r.Status().Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			delete(sec.Annotations, credentialTargetAnnotation)
			if tc.legacy {
				delete(sec.Annotations, credentialInstanceAnnotation)
			}
			sec.Labels[instanceLabelKey] = inst.Name
			sec.Labels[slugAnnotationKey] = inst.Annotations[slugAnnotationKey]
			if err := r.Update(ctx, sec); err != nil {
				t.Fatal(err)
			}
			replacement := inst.DeepCopy()
			replacement.Name, replacement.UID, replacement.ResourceVersion = "other-hosted", "hosted-b", ""
			if tc.pending {
				replacement.Status.Conditions[0].Status = metav1.ConditionFalse
			}
			if err := r.Create(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(tr), tr); err != nil {
				t.Fatal(err)
			}
			tr.Spec.InstanceName = replacement.Name
			if err := r.Update(ctx, tr); err != nil {
				t.Fatal(err)
			}
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			if tc.pending {
				if string(sec.Data[openAIAPIKeyKey]) != tokenFixtureKey || sec.Labels[instanceLabelKey] != inst.Name {
					t.Fatal("Pending hosted retarget erased the previous key binding before rotation")
				}
				if string(sec.Data[openAIAPIURLKey]) != unavailableAPIURL {
					t.Fatal("Pending hosted retarget kept its old route available")
				}
				setInstanceReadyStatus(replacement, tokenFixtureHTTPSURL)
				if err := r.Status().Update(ctx, replacement); err != nil {
					t.Fatal(err)
				}
				if err := reconcileTokenEndpoint(t, r, tr); err != nil {
					t.Fatal(err)
				}
				if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
					t.Fatal(err)
				}
			}
			if string(sec.Data[openAIAPIKeyKey]) == tokenFixtureKey || sec.Annotations[credentialInstanceAnnotation] != string(replacement.UID) {
				t.Fatal("known hosted retarget retained its old instance key")
			}
			if sec.Annotations[credentialTargetAnnotation] != "" {
				t.Fatal("hosted key acquired a BYOC target binding")
			}
		})
	}
}

func TestLegacyHostedAmbiguousBindingRotatesKey(t *testing.T) {
	for _, scenario := range []string{"missing instance label", "missing slug label", "different slug"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, inst, tr, sec := tokenEndpointFixture(t)
			inst.Spec.ClusterRef = nil
			if err := r.Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			inst.Status.ActiveBYOCTargetID = ""
			if err := r.Status().Update(ctx, inst); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			delete(sec.Annotations, credentialTargetAnnotation)
			delete(sec.Annotations, credentialInstanceAnnotation)
			sec.Labels[instanceLabelKey] = inst.Name
			sec.Labels[slugAnnotationKey] = inst.Annotations[slugAnnotationKey]
			switch scenario {
			case "missing instance label":
				delete(sec.Labels, instanceLabelKey)
			case "missing slug label":
				delete(sec.Labels, slugAnnotationKey)
			case "different slug":
				sec.Labels[slugAnnotationKey] = "previous-instance-slug"
			}
			if err := r.Update(ctx, sec); err != nil {
				t.Fatal(err)
			}
			if err := reconcileTokenEndpoint(t, r, tr); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(sec), sec); err != nil {
				t.Fatal(err)
			}
			if string(sec.Data[openAIAPIKeyKey]) == tokenFixtureKey {
				t.Fatal("ambiguous legacy hosted binding retained its key")
			}
			if sec.Annotations[credentialInstanceAnnotation] != string(inst.UID) {
				t.Fatal("replacement key lacks current instance UID")
			}
		})
	}
}
