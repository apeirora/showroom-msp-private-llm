package slugpolicy

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestImmutableSelectionAndReviewedLegacyMigration(t *testing.T) {
	policy, err := Parse(strings.NewReader(`{"cutover":"2026-10-28T12:00:00Z","entries":[{"uid":"old-uid","namespace":"tenant-a","name":"old","mode":"legacy","slug":"Existing_Slug"},{"uid":"unused-uid","namespace":"tenant-a","name":"unused","mode":"derive"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	old := &metav1.ObjectMeta{Name: "old", Namespace: "tenant-a", UID: "old-uid", CreationTimestamp: metav1.NewTime(time.Unix(100, 0))}
	selection, err := policy.Select(old)
	if err != nil || selection.Slug != "Existing_Slug" || selection.Source != Legacy {
		t.Fatalf("legacy selection=%+v err=%v", selection, err)
	}
	old.Annotations = map[string]string{"llm.privatellms.msp/slug": "victim"}
	if got, err := policy.Select(old); err != nil || got != selection {
		t.Fatal("tenant metadata changed the reviewed legacy selection")
	}
	old.Name = "different"
	if _, err := policy.Select(old); err == nil {
		t.Fatal("legacy UID map accepted a different object name")
	}
	old.Name, old.UID = "unused", "unused-uid"
	if got, err := policy.Select(old); err != nil || got.Source != Derived {
		t.Fatal("explicit never-provisioned migration failed")
	}
	old.Name, old.UID = "unmapped", "unmapped-uid"
	if _, err := policy.Select(old); err != ErrLegacyUnmapped {
		t.Fatal("unmapped old instance was silently renamed")
	}
	old.CreationTimestamp = metav1.NewTime(time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	if _, err := policy.Select(old); err != ErrLegacyUnmapped {
		t.Fatal("cutover boundary was not conservative")
	}
	old.CreationTimestamp = metav1.NewTime(time.Date(2026, 10, 28, 12, 0, 1, 0, time.UTC))
	first, err := policy.Select(old)
	if err != nil || first.Source != Derived || len(first.Slug) != 36 || !strings.HasPrefix(first.Slug, "llm-") {
		t.Fatalf("new identity not derived: %+v %v", first, err)
	}
	old.Annotations = nil
	old.Labels = map[string]string{"llm.privatellms.msp/slug": "chosen"}
	if got, err := policy.Select(old); err != nil || got != first {
		t.Fatal("new slug depends on mutable metadata")
	}
	old.UID = "recreated-uid"
	if got, err := policy.Select(old); err != nil || got.Slug == first.Slug {
		t.Fatal("replacement UID inherited the old slug")
	}
}

func TestRejectInvalidAndAmbiguousPolicies(t *testing.T) {
	entry := `{"uid":"first-uid","namespace":"tenant-a","name":"model","mode":"legacy","slug":"safe"}`
	cases := []string{`{"cutover":"2026-10-28T12:00:00Z","entries":[{"uid":"one","namespace":"tenant-a","name":"model","mode":"legacy","slug":"first","\u017Flug":"second"}]}`, `{"cutover":"2026-10-28T12:00:00Z","CUTOVER":"2026-10-29T12:00:00Z"}`, `{"cutover":"2026-10-28T12:00:00Z","entries":[{"uid":"one","UID":"two","namespace":"tenant-a","name":"model","mode":"legacy","slug":"safe"}]}`, `{"Cutover":"2026-10-28T12:00:00Z"}`, `{"cutover":"2026-10-28T12:00:00Z","cutover":"2026-10-29T12:00:00Z"}`, `{"cutover":"2026-10-28T12:00:00Z","entries":[{"uid":"one","uid":"two","namespace":"tenant-a","name":"model","mode":"legacy","slug":"safe"}]}`, `{}`, `{"cutover":"invalid"}`, `{"cutover":"2026-10-28T12:00:00Z","unknown":true}`, `{"cutover":"2026-10-28T12:00:00Z"} {}`}
	for _, bad := range []string{"", "with/slash", "x` || Path(`/", "llm-abcdef", "llm", "l", "..", strings.Repeat("a", 64)} {
		cases = append(cases, `{"cutover":"2026-10-28T12:00:00Z","entries":[`+strings.Replace(entry, `"safe"`, `"`+bad+`"`, 1)+`]}`)
	}
	for _, extra := range []string{entry, strings.NewReplacer(`"first-uid"`, `"other-uid"`, `"model"`, `"other"`).Replace(entry), strings.NewReplacer(`"first-uid"`, `"other-uid"`, `"model"`, `"other"`, `"safe"`, `"safe-shadow"`).Replace(entry)} {
		cases = append(cases, `{"cutover":"2026-10-28T12:00:00Z","entries":[`+entry+`,`+extra+`]}`)
	}
	for i, input := range cases {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("accepted ambiguous/invalid policy case %d", i)
		}
	}
	if _, err := Parse(strings.NewReader(strings.Repeat("x", (1<<20)+1))); err == nil {
		t.Fatal("unbounded policy accepted")
	}
	var absent *Policy
	if _, err := absent.Select(&metav1.ObjectMeta{}); err == nil {
		t.Fatal("missing policy did not fail closed")
	}
}
