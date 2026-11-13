package controller

import (
	"context"
	"strings"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func endpointSliceFixture(name, namespace, service string, port int32, ready ...*bool) *discoveryv1.EndpointSlice {
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       []discoveryv1.EndpointPort{{Port: &port}},
	}
	for _, condition := range ready {
		slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
			Addresses:  []string{"10.0.0.10"},
			Conditions: discoveryv1.EndpointConditions{Ready: condition},
		})
	}
	return slice
}

func TestServiceHasReadyEndpoints(t *testing.T) {
	const (
		namespace = "tenant-a"
		service   = "llama-model"
		port      = int32(8000)
	)
	yes, no := true, false

	tests := []struct {
		name        string
		slices      []client.Object
		wantReady   bool
		wantMessage string
	}{
		{
			name:        "no slice for the service",
			wantMessage: `EndpointSlices for Service "llama-model" not found`,
		},
		{
			name:        "slice of another service",
			slices:      []client.Object{endpointSliceFixture("other", namespace, "other", port, &yes)},
			wantMessage: `EndpointSlices for Service "llama-model" not found`,
		},
		{
			name:        "same service name in another namespace",
			slices:      []client.Object{endpointSliceFixture("a", "tenant-b", service, port, &yes)},
			wantMessage: `EndpointSlices for Service "llama-model" not found`,
		},
		{
			name:        "slice without endpoints",
			slices:      []client.Object{endpointSliceFixture("a", namespace, service, port)},
			wantMessage: "no ready endpoints on port 8000",
		},
		{
			name:        "endpoint not ready",
			slices:      []client.Object{endpointSliceFixture("a", namespace, service, port, &no)},
			wantMessage: "no ready endpoints on port 8000",
		},
		{
			name:        "ready endpoint on another port",
			slices:      []client.Object{endpointSliceFixture("a", namespace, service, 9000, &yes)},
			wantMessage: "no ready endpoints on port 8000",
		},
		{
			name:      "ready endpoint",
			slices:    []client.Object{endpointSliceFixture("a", namespace, service, port, &yes)},
			wantReady: true,
		},
		{
			name:      "unset ready condition counts as ready",
			slices:    []client.Object{endpointSliceFixture("a", namespace, service, port, nil)},
			wantReady: true,
		},
		{
			name: "ready endpoint in a second slice",
			slices: []client.Object{
				endpointSliceFixture("a", namespace, service, port, &no),
				endpointSliceFixture("b", namespace, service, port, &no, &yes),
			},
			wantReady: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sch := runtime.NewScheme()
			if err := discoveryv1.AddToScheme(sch); err != nil {
				t.Fatal(err)
			}
			r := &LLMInstanceReconciler{
				Client: clientfake.NewClientBuilder().WithScheme(sch).WithObjects(tt.slices...).Build(),
			}

			ready, message, err := r.serviceHasReadyEndpoints(context.Background(), namespace, service, port)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ready != tt.wantReady {
				t.Fatalf("ready = %v, want %v (message %q)", ready, tt.wantReady, message)
			}
			if !strings.Contains(message, tt.wantMessage) {
				t.Fatalf("message = %q, want it to contain %q", message, tt.wantMessage)
			}
			if tt.wantReady && message != "" {
				t.Fatalf("ready result carries a message: %q", message)
			}
		})
	}
}
