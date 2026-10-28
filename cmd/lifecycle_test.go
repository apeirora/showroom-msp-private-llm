package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/apeirora/showroom-msp-private-llm/internal/auth"
)

func TestOperatorFailsWhenAuthPortIsOccupied(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	managerStopped := false
	err = runOperator(ctx, func(ctx context.Context) error {
		<-ctx.Done()
		managerStopped = true
		return nil
	}, func(ctx context.Context) error {
		return (&auth.Server{}).Start(ctx, listener.Addr().String())
	})
	var bindErr *net.OpError
	if !errors.As(err, &bindErr) || bindErr.Op != "listen" {
		t.Fatalf("auth bind failure was not returned: %v", err)
	}
	if ctx.Err() != nil || !managerStopped {
		t.Fatal("auth bind failure did not stop the manager before the parent deadline")
	}
}

func TestOperatorManagerExitCancelsAndJoinsAuth(t *testing.T) {
	managerFailure := errors.New("synthetic manager failure")
	for _, tc := range []struct {
		name string
		err  error
	}{{"clean exit", nil}, {"failure", managerFailure}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			authStarted := make(chan struct{})
			authCleanedUp := false
			err := runOperator(ctx, func(context.Context) error {
				<-authStarted
				return tc.err
			}, func(ctx context.Context) error {
				close(authStarted)
				<-ctx.Done()
				authCleanedUp = true
				return http.ErrServerClosed
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("manager result was lost: %v", err)
			}
			if ctx.Err() != nil || !authCleanedUp {
				t.Fatal("manager exit did not wait for canceled auth cleanup")
			}
		})
	}
}

func TestOperatorSignalCancellationIsClean(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	authStarted := make(chan struct{})
	err := runOperator(ctx, func(ctx context.Context) error {
		<-authStarted
		cancel() // Models the single process signal context being canceled.
		<-ctx.Done()
		return nil
	}, func(ctx context.Context) error {
		close(authStarted)
		return (&auth.Server{}).Start(ctx, "127.0.0.1:0")
	})
	if err != nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("normal signal cancellation failed: %v", err)
	}
}

func TestOperatorPreservesBothServiceErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	managerFailure := errors.New("synthetic manager failure")
	authFailure := errors.New("synthetic auth listener failure")
	managerStarted := make(chan struct{})
	err := runOperator(ctx, func(ctx context.Context) error {
		close(managerStarted)
		<-ctx.Done()
		return managerFailure
	}, func(context.Context) error {
		<-managerStarted
		return authFailure
	})
	if !errors.Is(err, managerFailure) || !errors.Is(err, authFailure) {
		t.Fatalf("one of the service errors was discarded: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("auth failure required the parent deadline to stop the manager")
	}
}

func TestOperatorRejectsUnexpectedAuthExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"nil", nil}, {"server closed", http.ErrServerClosed}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := runOperator(ctx, func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}, func(context.Context) error { return tc.err })
			if err == nil || ctx.Err() != nil {
				t.Fatalf("unexpected auth exit did not fail and cancel manager: %v", err)
			}
		})
	}
}
