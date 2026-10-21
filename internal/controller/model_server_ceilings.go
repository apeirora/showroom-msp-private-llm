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
	"fmt"
	"strconv"

	llmv1alpha1 "github.com/apeirora/showroom-msp-private-llm/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ModelServerCeilings is provider policy, not a tenant API setting.
// Zero uses the model's existing explicit budget, never an unlimited override.
type ModelServerCeilings struct {
	CPUMilli      int
	MemoryMiB     int
	ContextSize   int
	ParallelSlots int
}

// Validate rejects invalid provider configuration before the manager starts.
func (c ModelServerCeilings) Validate() error {
	for _, limit := range []struct {
		name           string
		value, maximum int
	}{
		{"max-model-cpu-milli", c.CPUMilli, 16000},
		{"max-model-memory-mib", c.MemoryMiB, 65536},
		{"max-context-size", c.ContextSize, 131072},
		{"max-parallel-slots", c.ParallelSlots, 16},
	} {
		if limit.value < 0 || limit.value > limit.maximum {
			return fmt.Errorf("%s must be between 0 and %d", limit.name, limit.maximum)
		}
	}
	return nil
}

func (c ModelServerCeilings) check(inst *llmv1alpha1.LLMInstance) error {
	if err := c.Validate(); err != nil {
		return err
	}
	baseline := resolveModel(inst.Spec.Model)
	desired := modelForInstance(inst)
	cpuRequestMax, cpuLimitMax := int64(c.CPUMilli), int64(c.CPUMilli)
	if c.CPUMilli == 0 {
		cpuRequestMax, cpuLimitMax = cpuMilli(baseline.cpuRequest), cpuMilli(baseline.cpuLimit)
	}
	memoryRequestMax, memoryLimitMax := int64(c.MemoryMiB), int64(c.MemoryMiB)
	if c.MemoryMiB == 0 {
		memoryRequestMax, memoryLimitMax = memoryMiB(baseline.memoryRequest), memoryMiB(baseline.memoryLimit)
	}
	contextMax := c.ContextSize
	if contextMax == 0 {
		contextMax = serverIntFlag(baseline.serverArgs, "--ctx-size", 0)
	}
	parallelMax := c.ParallelSlots
	// The pinned llama-server defaults to four slots. Keep that existing budget.
	if parallelMax == 0 {
		parallelMax = serverIntFlag(baseline.serverArgs, "--parallel", 4)
	}
	for _, limit := range []struct {
		name               string
		requested, maximum int64
	}{
		{"CPU request (millicores)", cpuMilli(desired.cpuRequest), cpuRequestMax},
		{"CPU limit (millicores)", cpuMilli(desired.cpuLimit), cpuLimitMax},
		{"memory request (MiB)", memoryMiB(desired.memoryRequest), memoryRequestMax},
		{"memory limit (MiB)", memoryMiB(desired.memoryLimit), memoryLimitMax},
		{"contextSize", int64(serverIntFlag(desired.serverArgs, "--ctx-size", 0)), int64(contextMax)},
		{"parallelSlots", int64(serverIntFlag(desired.serverArgs, "--parallel", 4)), int64(parallelMax)},
	} {
		if limit.requested > limit.maximum {
			return fmt.Errorf("%s %d exceeds provider ceiling %d; ask the provider to raise the ceiling", limit.name, limit.requested, limit.maximum)
		}
	}
	return nil
}

func cpuMilli(value string) int64 {
	if value == "" {
		return 0
	}
	quantity := resource.MustParse(value)
	return quantity.MilliValue()
}

func memoryMiB(value string) int64 {
	if value == "" {
		return 0
	}
	quantity := resource.MustParse(value)
	return quantity.Value() / (1024 * 1024)
}

func serverIntFlag(args []string, name string, fallback int) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			value, err := strconv.Atoi(args[i+1])
			if err == nil {
				return value
			}
		}
	}
	return fallback
}
