package controller

import (
	"bytes"
	"context"
	"errors"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const cacheTestSlug = "cache-race-instance"

var errCacheWrite = errors.New("synthetic credential-cache write failure")

type byocCacheTestClient struct {
	client.Client
	sourceKey, cacheKey client.ObjectKey
	sourceReads         int
	failCacheWrite      bool
}

func (c *byocCacheTestClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok && key == c.sourceKey {
		c.sourceReads++
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *byocCacheTestClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if c.failCacheWrite && client.ObjectKeyFromObject(obj) == c.cacheKey {
		return errCacheWrite
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *byocCacheTestClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.failCacheWrite && client.ObjectKeyFromObject(obj) == c.cacheKey {
		return errCacheWrite
	}
	return c.Client.Update(ctx, obj, opts...)
}

type byocIdentityReadHook struct {
	client.Client
	afterRead func()
}

func (c *byocIdentityReadHook) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if err == nil && key.Name == "kube-system" && c.afterRead != nil {
		c.afterRead()
		c.afterRead = nil
	}
	return err
}

type byocCacheFixture struct {
	r           *LLMInstanceReconciler
	local       *byocCacheTestClient
	remote      *byocIdentityReadHook
	inst        *llmv1alpha1.LLMInstance
	old, next   []byte
	oldTargetID string
}

func newBYOCCacheFixture(t *testing.T, active bool) byocCacheFixture {
	t.Helper()
	ctx := context.Background()
	sch := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, llmv1alpha1.AddToScheme} {
		if err := add(sch); err != nil {
			t.Fatal(err)
		}
	}
	ca := newBYOCTLSTestAuthority(t)
	config := byocIntegrationFixture{ca: ca}
	f := byocCacheFixture{old: []byte(config.kubeconfig("old")), next: []byte(config.kubeconfig("replacement"))}
	f.inst = &llmv1alpha1.LLMInstance{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "tenant", UID: "instance-a", Generation: 1,
		Annotations: map[string]string{slugAnnotationKey: cacheTestSlug}}, Spec: llmv1alpha1.LLMInstanceSpec{ClusterRef: &llmv1alpha1.ClusterRef{KubeconfigSecretName: "remote"}}}
	f.remote = &byocIdentityReadHook{Client: fake.NewClientBuilder().WithScheme(sch).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "old-cluster"}}).Build()}
	apiID, err := byocTargetIdentity(f.old)
	if err != nil {
		t.Fatal(err)
	}
	f.oldTargetID, err = byocClusterIdentity(ctx, f.remote, apiID)
	if err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{f.inst, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "remote", Namespace: f.inst.Namespace}, Data: map[string][]byte{byocKubeconfigKey: f.old}}}
	if active {
		f.inst.Status.ActiveBYOCKubeconfigSecretName = f.inst.Spec.ClusterRef.KubeconfigSecretName
		f.inst.Status.ActiveBYOCTargetID, f.inst.Status.ActiveBYOCSlug = f.oldTargetID, cacheTestSlug
		cache := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: byocCredentialsName(cacheTestSlug), Namespace: f.inst.Namespace}, Data: map[string][]byte{byocKubeconfigKey: f.old}}
		if err := ctrl.SetControllerReference(f.inst, cache, sch); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, cache)
	}
	f.local = &byocCacheTestClient{Client: fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(f.inst).WithObjects(objects...).Build(),
		sourceKey: client.ObjectKey{Namespace: f.inst.Namespace, Name: "remote"}, cacheKey: client.ObjectKey{Namespace: f.inst.Namespace, Name: byocCredentialsName(cacheTestSlug)}}
	f.r = &LLMInstanceReconciler{Client: f.local, Scheme: sch, BYOCTLS: BYOCTLSConfig{Domain: "byoc.example.test", Namespace: "tls-system", IssuerName: "issuer", IssuerNamespace: "kube-system", DNSClass: "garden"},
		RemoteClientBuilder: func([]byte) (client.Client, error) { return f.remote, nil }}
	return f
}

func (f byocCacheFixture) replaceSource(t *testing.T) {
	t.Helper()
	var source corev1.Secret
	if err := f.local.Client.Get(context.Background(), f.local.sourceKey, &source); err != nil {
		t.Fatal(err)
	}
	source.Data[byocKubeconfigKey] = f.next
	if err := f.local.Client.Update(context.Background(), &source); err != nil {
		t.Fatal(err)
	}
}

func (f byocCacheFixture) assertOldCache(t *testing.T) {
	t.Helper()
	var cached corev1.Secret
	if err := f.local.Client.Get(context.Background(), f.local.cacheKey, &cached); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached.Data[byocKubeconfigKey], f.old) {
		t.Fatal("same-name source replacement overwrote previous cleanup credentials")
	}
	var stored llmv1alpha1.LLMInstance
	if err := f.local.Get(context.Background(), client.ObjectKeyFromObject(f.inst), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.ActiveBYOCTargetID != f.oldTargetID {
		t.Fatal("active target identity changed before cleanup")
	}
}

func TestBYOCCacheUsesValidatedSnapshot(t *testing.T) {
	for _, stage := range []string{"client construction", "cluster UID verification"} {
		t.Run(stage, func(t *testing.T) {
			f := newBYOCCacheFixture(t, true)
			if stage == "client construction" {
				f.r.RemoteClientBuilder = func(data []byte) (client.Client, error) {
					if !bytes.Equal(data, f.old) {
						t.Fatal("client did not use original snapshot")
					}
					f.replaceSource(t)
					return f.remote, nil
				}
			} else {
				f.remote.afterRead = func() { f.replaceSource(t) }
			}
			_, err := f.r.reconcileBYOC(context.Background(), f.inst, cacheTestSlug, resolveModel("phi-2"))
			f.assertOldCache(t)
			if err != nil {
				t.Fatal(err)
			}
			if f.local.sourceReads != 1 {
				t.Fatalf("provisioning reread the mutable source %d times", f.local.sourceReads)
			}
		})
	}
}

func TestBYOCCacheRejectsChangedTargetBeforeWrite(t *testing.T) {
	f := newBYOCCacheFixture(t, true)
	f.replaceSource(t) // Simulates replacement after the outer cleanup check.
	_, err := f.r.reconcileBYOC(context.Background(), f.inst, cacheTestSlug, resolveModel("phi-2"))
	if err == nil {
		t.Fatal("changed active target was accepted")
	}
	f.assertOldCache(t)
}

func TestBYOCCacheFailureDoesNotRecordTarget(t *testing.T) {
	f := newBYOCCacheFixture(t, false)
	f.local.failCacheWrite = true
	_, err := f.r.reconcileBYOC(context.Background(), f.inst, cacheTestSlug, resolveModel("phi-2"))
	if !errors.Is(err, errCacheWrite) {
		t.Fatalf("expected cache write failure, got %v", err)
	}
	var stored llmv1alpha1.LLMInstance
	if err := f.local.Get(context.Background(), client.ObjectKeyFromObject(f.inst), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.ActiveBYOCTargetID != "" || stored.Status.ActiveBYOCKubeconfigSecretName != "" {
		t.Fatal("target was recorded without cleanup credentials")
	}
	if err := f.local.Get(context.Background(), f.local.cacheKey, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("failed cache creation unexpectedly persisted: %v", err)
	}
	// A retry can still establish the target because failed persistence did not
	// make cleanup depend on a nonexistent cache.
	f.local.failCacheWrite = false
	*f.inst = stored
	if _, err := f.r.reconcileBYOC(context.Background(), f.inst, cacheTestSlug, resolveModel("phi-2")); err != nil {
		t.Fatal(err)
	}
	f.assertOldCache(t)
}
