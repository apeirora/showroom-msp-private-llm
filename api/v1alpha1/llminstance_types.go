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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// LLMInstanceSpec defines the desired state of LLMInstance
type LLMInstanceSpec struct {
	// Model is the desired model identifier (e.g., "TinyLlama/TinyLlama-1.1B-Chat-v1.0").
	// +kubebuilder:validation:Enum=tinyllama;phi2
	// +kubebuilder:default=tinyllama
	Model string `json:"model,omitempty"`

	// Replicas indicates how many server pods to run.
	// Defaults to 1 if omitted or set to 0.
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas,omitempty"`
}

// LLMInstanceStatus defines the observed state of LLMInstance
type LLMInstanceStatus struct {
	// Phase is a high-level summary of the instance lifecycle (e.g., "Ready").
	Phase string `json:"phase,omitempty"`

	// Endpoint is the effective inference endpoint being used.
	Endpoint string `json:"endpoint,omitempty"`

	// ObservedGeneration reflects the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`

// LLMInstance is the Schema for the llminstances API
type LLMInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LLMInstanceSpec   `json:"spec,omitempty"`
	Status LLMInstanceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// LLMInstanceList contains a list of LLMInstance
type LLMInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LLMInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMInstance{}, &LLMInstanceList{})
}
