package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContractAndErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("Idempotency-Key") != "invoice-123" {
			t.Error("missing credentials or idempotency")
		}
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	defer s.Close()
	c, e := New(s.URL, "key")
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Send(context.Background(), "invoice-123", SendRequest{})
	var apiError *APIError
	if !errors.As(e, &apiError) || apiError.Status != 503 || apiError.RetryAfter != "3" {
		t.Fatalf("%v", e)
	}
	if _, e = c.Send(context.Background(), "", SendRequest{}); e == nil {
		t.Fatal("empty idempotency key accepted")
	}
}
