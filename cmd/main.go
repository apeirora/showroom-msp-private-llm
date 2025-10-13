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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"

	llmv1alpha1 "github.com/example/private-llm/api/v1alpha1"
	"github.com/example/private-llm/internal/auth"
	"github.com/example/private-llm/internal/controller"
	//+kubebuilder:scaffold:imports
)

// RBAC for manager-level helpers and services.
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
//+kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// version is set at build time via: -ldflags "-X main.version=<version>"
var version string

const (
	// Default values for Kubernetes recommended labels
	defaultAppName   = "private-llm"
	defaultPartOf    = "private-llm"
	defaultManagedBy = "private-llm-operator"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(llmv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// labelingClient wraps a controller-runtime client to inject Kubernetes recommended
// labels into all created/updated/patched resources managed by this operator.
type labelingClient struct {
	client.Client
	baseLabels map[string]string
}

func newLabelingClient(delegate client.Client, base map[string]string) client.Client {
	return &labelingClient{Client: delegate, baseLabels: base}
}

func (c *labelingClient) ensureLabels(obj client.Object) {
	if obj == nil || c == nil || len(c.baseLabels) == 0 {
		return
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range c.baseLabels {
		if v == "" {
			continue
		}
		if _, exists := labels[k]; !exists {
			labels[k] = v
		}
	}
	obj.SetLabels(labels)
}

func (c *labelingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.ensureLabels(obj)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *labelingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.ensureLabels(obj)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *labelingClient) Patch(
	ctx context.Context,
	obj client.Object,
	patch client.Patch,
	opts ...client.PatchOption,
) error {
	c.ensureLabels(obj)
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	var authBind string
	var authExternalURL string
	flag.StringVar(&authBind, "auth-bind-address", ":8090", "Address for internal auth server")
	flag.StringVar(
		&authExternalURL,
		"auth-external-url",
		"",
		"External URL used by Traefik to reach auth server (default derives from POD_NAMESPACE)",
	)
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if shutdown, err := initOpenTelemetry(context.Background()); err != nil {
		setupLog.Error(err, "opentelemetry init failed")
	} else if shutdown != nil {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = shutdown(ctx)
			cancel()
		}()
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "953016b8.example.com",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Build base labels for all operator-managed resources
	// Kubernetes recommended labels: https://kubernetes.io/docs/concepts/overview/working-with-objects/common-labels/
	baseLabels := map[string]string{
		"app.kubernetes.io/name":       defaultAppName,
		"app.kubernetes.io/part-of":    defaultPartOf,
		"app.kubernetes.io/managed-by": defaultManagedBy,
	}
	if version != "" {
		baseLabels["app.kubernetes.io/version"] = version
	}

	labeledClient := newLabelingClient(mgr.GetClient(), baseLabels)

	// Start lightweight auth server that validates Bearer tokens against Secrets
	authServer := &auth.Server{K8sClient: labeledClient}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := authServer.Start(ctx, authBind); err != nil {
			setupLog.Error(err, "auth server stopped")
		}
	}()

	if err = (&controller.LLMInstanceReconciler{
		Client: labeledClient,
		Scheme: mgr.GetScheme(),
		PublicHost: func() string {
			if v := os.Getenv("PUBLIC_HOST"); v != "" {
				return v
			}
			return "localhost"
		}(),
		AuthServiceURL: func() string {
			if authExternalURL != "" {
				return authExternalURL
			}
			// derive from service DNS using runtime namespace
			ns := os.Getenv("POD_NAMESPACE")
			if ns == "" {
				ns = "default"
			}
			return "http://private-llm-controller-manager." + ns + ".svc.cluster.local:8090"
		}(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LLMInstance")
		os.Exit(1)
	}

	if err = (&controller.TokenRequestReconciler{
		Client: labeledClient,
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TokenRequest")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initOpenTelemetry(ctx context.Context) (func(context.Context) error, error) {
	var (
		exp tracesdk.SpanExporter
		err error
	)
	// Fallback to stdout exporter when no OTLP endpoint is configured.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		exp, err = stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
	} else {
		// Create OTLP/HTTP exporter (uses env vars by default).
		exp, err = otlptracehttp.New(ctx)
	}
	if err != nil {
		return nil, err
	}

	// Describe this service for backends.
	res, rerr := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", "private-llm-operator"),
		),
	)
	if rerr != nil {
		// Continue with a minimal resource rather than failing completely.
		res = resource.Empty()
	}

	tp := tracesdk.NewTracerProvider(
		tracesdk.WithBatcher(exp),
		tracesdk.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	return tp.Shutdown, nil
}
