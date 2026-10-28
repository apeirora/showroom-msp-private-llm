package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

func TestAuthRejectsCredentialFromPreviousInstanceOrTarget(t *testing.T) {
	for _, previous := range []string{"instance", "BYOC target", "unbound legacy key", "BYOC mode", "BYOC cleanup"} {
		t.Run(previous, func(t *testing.T) {
			inst, tr, sec := authFixture()
			target := boundURL
			switch previous {
			case "instance":
				inst.UID = "replacement-instance"
				target = "/auth/verify?slug=victim-slug&namespace=tenant-a&instance=model&uid=replacement-instance"
			case "BYOC target":
				sec.Annotations[credentialTargetKey] = "old-byoc-cluster"
			case "unbound legacy key":
				delete(sec.Annotations, credentialInstanceKey)
			case "BYOC mode":
				inst.Spec.ClusterRef = &llmv1alpha1.ClusterRef{KubeconfigSecretName: "remote-cluster"}
			case "BYOC cleanup":
				inst.Status.ActiveBYOCKubeconfigSecretName = "old-remote-cluster"
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			rr := httptest.NewRecorder()
			(&Server{SlugPolicy: testAuthSlugPolicy(), K8sClient: authClient(t, inst, tr, sec)}).Handler(context.Background()).ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("previous credential accepted on replacement hosted route: status = %d", rr.Code)
			}
		})
	}
}
