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

// TokenRequestSpec defines the desired state of TokenRequest
type TokenRequestSpec struct {
	// InstanceName references an LLMInstance in the same namespace.
	// +kubebuilder:validation:MinLength=1
	InstanceName string `json:"instanceName"`

	// Description is an optional human-friendly description for this token.
	// +optional
	Description string `json:"description,omitempty"`
}

// TokenRequestStatus defines the observed state of TokenRequest.
type TokenRequestStatus struct {
	// Phase is a high-level status summary (e.g., "Ready").
	// +optional
	Phase string `json:"phase,omitempty"`

	// SecretName is the name of the Secret containing the generated token.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// ObservedGeneration reflects the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.instanceName`
//+kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// TokenRequest is the Schema for the tokenrequests API
type TokenRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TokenRequestSpec   `json:"spec,omitempty"`
	Status TokenRequestStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TokenRequestList contains a list of TokenRequest
type TokenRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TokenRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TokenRequest{}, &TokenRequestList{})
}
