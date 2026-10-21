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
	"testing"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
)

func TestModelCeilings(t *testing.T) {
	for _, name := range []string{tinyLlamaModel, "phi-2", "gemma-3-1b-it", "gemma-3-4b-it", "gemma-3-12b-it", "qwen3-4b"} {
		inst := &llmv1alpha1.LLMInstance{Spec: llmv1alpha1.LLMInstanceSpec{Model: name}}
		if err := (ModelServerCeilings{}).check(inst); err != nil {
			t.Fatalf("default %s rejected: %v", name, err)
		}
	}
	cases := []struct {
		name   string
		spec   llmv1alpha1.LLMInstanceSpec
		raised ModelServerCeilings
	}{
		{"cpu", llmv1alpha1.LLMInstanceSpec{Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 6000, LimitMilli: 6000}}}, ModelServerCeilings{CPUMilli: 6000}},
		{"memory", llmv1alpha1.LLMInstanceSpec{Resources: &llmv1alpha1.ModelServerResources{Memory: &llmv1alpha1.ModelMemoryResources{RequestMiB: 8192, LimitMiB: 8192}}}, ModelServerCeilings{MemoryMiB: 8192}},
		{"context", llmv1alpha1.LLMInstanceSpec{ContextSize: ptrTo(int32(16384))}, ModelServerCeilings{ContextSize: 16384}},
		{"parallel", llmv1alpha1.LLMInstanceSpec{ParallelSlots: ptrTo(int32(8))}, ModelServerCeilings{ParallelSlots: 8}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Model = "qwen3-4b"
			inst := &llmv1alpha1.LLMInstance{Spec: tc.spec}
			if err := (ModelServerCeilings{}).check(inst); err == nil {
				t.Fatal("default ceiling accepted increased tenant budget")
			}
			if err := tc.raised.check(inst); err != nil {
				t.Fatalf("provider-raised ceiling rejected: %v", err)
			}
			inst.Spec.Model = tinyLlamaModel
			if err := (ModelServerCeilings{}).check(inst); err == nil {
				t.Fatal("legacy model accepted an increase without provider policy")
			}
		})
	}
	// A tenant cannot raise its reservation to the old limit without provider approval.
	reservation := &llmv1alpha1.LLMInstance{Spec: llmv1alpha1.LLMInstanceSpec{Model: "qwen3-4b", Resources: &llmv1alpha1.ModelServerResources{CPU: &llmv1alpha1.ModelCPUResources{RequestMilli: 1000, LimitMilli: 1800}}}}
	if err := (ModelServerCeilings{}).check(reservation); err == nil {
		t.Fatal("default policy permitted a larger CPU reservation")
	}
	reservation.Spec.Resources.CPU = nil
	reservation.Spec.Resources.Memory = &llmv1alpha1.ModelMemoryResources{RequestMiB: 5000, LimitMiB: 7168}
	if err := (ModelServerCeilings{}).check(reservation); err == nil {
		t.Fatal("default policy permitted a larger memory reservation")
	}
	// Provider policy also applies to an implicit model default.
	if err := (ModelServerCeilings{CPUMilli: 1000}).check(&llmv1alpha1.LLMInstance{Spec: llmv1alpha1.LLMInstanceSpec{Model: "qwen3-4b"}}); err == nil {
		t.Fatal("implicit default bypassed provider ceiling")
	}
}

func TestInvalidProviderCeilings(t *testing.T) {
	for _, c := range []ModelServerCeilings{{CPUMilli: -1}, {CPUMilli: 16001}, {MemoryMiB: -1}, {MemoryMiB: 65537}, {ContextSize: -1}, {ContextSize: 131073}, {ParallelSlots: -1}, {ParallelSlots: 17}} {
		if c.Validate() == nil {
			t.Fatalf("invalid provider configuration accepted: %+v", c)
		}
	}
}
