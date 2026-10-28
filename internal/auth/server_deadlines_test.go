package auth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAuthServerRejectsSlowHeadersAndServesNormalRequests(t *testing.T) {
	inst, tr, sec := authFixture()
	srv := (&Server{K8sClient: authClient(t, inst, tr, sec)}).httpServer(context.Background(), "")
	for name, timeout := range map[string]time.Duration{
		"headers": srv.ReadHeaderTimeout, "reads": srv.ReadTimeout,
		"writes": srv.WriteTimeout, "idle": srv.IdleTimeout,
	} {
		if timeout <= 0 || timeout > time.Minute {
			t.Fatalf("%s lacks a short finite deadline: %s", name, timeout)
		}
	}
	// Exercise the same server with a shorter header window, without sleeping
	// through the production timeout or changing normal authorization behavior.
	srv.ReadHeaderTimeout = 50 * time.Millisecond
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-stopped; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("HTTP server stopped unexpectedly: %v", err)
		}
	})
	httpClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(httpClient.CloseIdleConnections)
	get := func() {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+boundURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("normal authorization failed: status=%d body=%q err=%v", res.StatusCode, body, err)
		}
	}
	get()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /auth/verify HTTP/1.1\r\nHost: localhost\r\nX-Slow: "); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("incomplete headers were not closed by server deadline: %v", err)
	}
	get()
}

type lookupReader struct {
	client.Reader
	get func(context.Context) error
}

func (r lookupReader) Get(ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return r.get(ctx)
}

func TestAuthLookupReceivesTotalDeadline(t *testing.T) {
	reader := lookupReader{get: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > 5*time.Second {
			t.Fatalf("API lookup has no bounded authorization deadline: %s", remaining)
		}
		return context.DeadlineExceeded
	}}
	req := httptest.NewRequest(http.MethodGet, boundURL, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	result := httptest.NewRecorder()
	(&Server{K8sClient: reader}).Handler(context.Background()).ServeHTTP(result, req)
	if result.Code != http.StatusInternalServerError {
		t.Fatalf("failed API lookup did not fail closed: HTTP %d", result.Code)
	}
}

func TestAuthLookupStopsWhenRequestIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	reader := lookupReader{get: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	req := httptest.NewRequest(http.MethodGet, boundURL, nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	result := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Server{K8sClient: reader}).Handler(context.Background()).ServeHTTP(result, req)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("API lookup never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not stop the API lookup")
	}
	if result.Code != http.StatusInternalServerError {
		t.Fatalf("canceled API lookup did not fail closed: HTTP %d", result.Code)
	}
}

func TestAuthServerStartStopsForCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- (&Server{}).Start(ctx, "127.0.0.1:0") }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("canceled listener did not shut down: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled listener did not finish shutdown")
	}
}

func TestAuthServerStartReturnsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() { done <- (&Server{}).Start(context.Background(), listener.Addr().String()) }()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("occupied listener did not report startup failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bind failure waited for context cancellation")
	}
}
