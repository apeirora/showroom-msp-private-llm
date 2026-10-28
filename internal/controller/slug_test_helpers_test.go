package controller

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	"github.com/apeirora/showroom-msp-private-llm/internal/slugpolicy"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	slugReadyPhase       = "Ready"
	testLegacySlugSource = slugpolicy.Legacy
	testKeptSlug         = "kept-slug"
)

func testDerivedSlugPolicy() *slugpolicy.Policy {
	p, err := slugpolicy.Parse(strings.NewReader(`{"cutover":"1970-01-01T00:00:00Z","entries":[]}`))
	if err != nil {
		panic(err)
	}
	return p
}

// Explicitly approve the known legacy identity of a synthetic test fixture.
// Production has no annotation-to-policy importer.
func testLegacySlugPolicy(instances ...*llmv1alpha1.LLMInstance) *slugpolicy.Policy {
	entries := []map[string]string{}
	for _, inst := range instances {
		if inst.CreationTimestamp.IsZero() {
			inst.CreationTimestamp = metav1.NewTime(time.Unix(100, 0))
		}
		slug := inst.Annotations[slugAnnotationKey]
		if slug == "" {
			continue
		}
		entries = append(entries, map[string]string{"uid": string(inst.UID), "namespace": inst.Namespace, "name": inst.Name, "mode": "legacy", "slug": slug})
	}
	data, err := json.Marshal(map[string]any{"cutover": "2030-01-01T00:00:00Z", "entries": entries})
	if err != nil {
		panic(err)
	}
	p, err := slugpolicy.Parse(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}
	return p
}
