package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFullEventPageWithDiagnostics(t *testing.T) {
	want := EventPage{Events: make([]Event, 1000), NextCursor: "1000"}
	for i := range want.Events {
		want.Events[i] = Event{ID: "event", Sequence: int64(i + 1), Details: map[string]string{"diagnostic": strings.Repeat("diagnostic ", 120)}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()
	c, e := New(server.URL, "test-key")
	if e != nil {
		t.Fatal(e)
	}
	got, e := c.Events(context.Background(), "message", "", 1000)
	if e != nil || len(got.Events) != 1000 || got.NextCursor != "1000" {
		t.Fatalf("truncated event page len=%d: %v", len(got.Events), e)
	}
}
