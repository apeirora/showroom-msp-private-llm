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
package auth

import (
	"context"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Server struct {
	K8sClient client.Client
}

// Start runs a small HTTP server handling /auth/verify to validate Authorization: Bearer tokens.
// It uses an opaque instance slug (no namespace exposure). The slug is taken from:
// - the 'slug' query parameter when provided (preferred)
// - or parsed from 'X-Forwarded-Uri' when the request path looks like '/llm/<slug>/...'
func (s *Server) Start(ctx context.Context, bindAddress string) error {
	mux := s.Handler(ctx)

	srv := &http.Server{Addr: bindAddress, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	return srv.ListenAndServe()
}

// Handler returns the HTTP handler for the auth server. Useful for tests.
func (s *Server) Handler(ctx context.Context) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		logger := log.FromContext(ctx)
		slug := r.URL.Query().Get("slug")
		if slug == "" {
			// Try to derive slug from forwarded URI: expected '/llm/<slug>/...'
			forwarded := r.Header.Get("X-Forwarded-Uri")
			if forwarded != "" {
				// normalize and split
				if strings.HasPrefix(forwarded, "/") {
					parts := strings.Split(forwarded, "/")
					// parts: ['', 'llm', '<slug>', ...]
					if len(parts) >= 3 && parts[1] == "llm" && parts[2] != "" {
						slug = parts[2]
					}
				}
			}
		}
		if slug == "" {
			http.Error(w, "missing slug", http.StatusBadRequest)
			return
		}
		// Parse Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		if token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "empty bearer token", http.StatusUnauthorized)
			return
		}
		// Find any Secret with matching token
		var secrets corev1.SecretList
		// Search cluster-wide by opaque slug label
		selector := labels.SelectorFromSet(map[string]string{
			"llm.privatellms.msp/slug": slug,
		})
		if err := s.K8sClient.List(ctx, &secrets, &client.ListOptions{LabelSelector: selector}); err != nil {
			logger.Error(err, "failed to list secrets by slug")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, sec := range secrets.Items {
			if val, ok := sec.Data["OPENAI_API_KEY"]; ok {
				if string(val) == token {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("ok"))
					return
				}
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer error=\"invalid_token\"")
		http.Error(w, "invalid token", http.StatusUnauthorized)
	})
	return mux
}
