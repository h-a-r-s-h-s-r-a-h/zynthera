package agentclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUnreachableAgent(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens here any more

	err := New(url, "t").Start(context.Background(), "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestProxyErrorsCountAsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))
	defer srv.Close()
	if err := New(srv.URL, "t").Health(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestDeadlineIsNotReportedAsUnavailable(t *testing.T) {
	// The handler hangs until the test is over, like an agent that is stuck.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release) // runs before srv.Close, so Close does not wait forever

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := New(srv.URL, "t").Start(ctx, "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want a deadline error", err)
	}
}

func TestErrorEnvelopeIsParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"driver_error","message":"The VM operation failed.","details":{"detail":"virsh: boom"}}}`))
	}))
	defer srv.Close()

	err := New(srv.URL, "t").Suspend(context.Background(), "11111111-1111-4111-8111-111111111111")
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != "driver_error" || ae.Status != 500 || ae.Detail != "virsh: boom" {
		t.Fatalf("error = %#v", err)
	}
	if IsBusy(err) || IsNotFound(err) {
		t.Error("wrong classification")
	}
}
