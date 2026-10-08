package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestPercentilesIncludeTailAndPreserveInput(t *testing.T) {
	values := []time.Duration{time.Second, time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	original := append([]time.Duration(nil), values...)
	got := summarize(values)
	if got.P50 != 2 || got.P95 != 1000 || got.P99 != 1000 || got.Max != 1000 || !reflect.DeepEqual(values, original) {
		t.Fatalf("misreported latency tail or mutated input: %+v %v", got, values)
	}
	if summarize(nil) != (distribution{}) {
		t.Fatal("empty workload has nonzero latency")
	}
}

func TestUnsafeOrOversizedWorkloadsRefused(t *testing.T) {
	base := options{Count: 120, Concurrency: 8, TextBytes: 1024, RecipientDomains: 1, Timeout: time.Minute}
	if e := base.validate("development", "http://api:8080", "http://sink:8025"); e != nil {
		t.Fatal(e)
	}
	for _, env := range []string{"production", ""} {
		if base.validate(env, "http://api:8080", "http://sink:8025") == nil {
			t.Fatal("unsafe environment allowed")
		}
	}
	if base.validate("development", "https://external.test", "http://sink:8025") == nil ||
		base.validate("development", "http://api:8080", "http://other:8025") == nil {
		t.Fatal("external target allowed")
	}
	large := base
	large.Count, large.AttachmentBytes = 1000, 2*1024*1024
	if large.validate("development", "http://api:8080", "http://sink:8025") == nil {
		t.Fatal("retained payload ceiling ignored")
	}
	invalid := base
	invalid.Concurrency = 65
	if invalid.validate("development", "http://api:8080", "http://sink:8025") == nil {
		t.Fatal("unbounded concurrency allowed")
	}
}

func TestOnlyCompleteErrorFreeOneCopyWorkloadPasses(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		copies, api  int
		errors       map[string]int
		pass         bool
	}{
		{"complete", "delivered", 1, 2, nil, true},
		{"API rejection", "delivered", 1, 1, map[string]int{"http_429": 1}, false},
		{"duplicate", "delivered", 2, 2, nil, false},
		{"missing copy", "delivered", 0, 2, nil, false},
		{"queued", "queued", 1, 2, nil, false},
		{"ambiguous", "submission_unknown", 1, 2, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := report{Requested: 2, APIAccepted: tc.api, APIErrors: tc.errors}
			now := time.Now()
			messages := []message{{"a", "delivered", now, now.Add(time.Second)}, {"b", tc.status, now, now.Add(2 * time.Second)}}
			err := r.verify(messages, map[string]struct{}{"a": {}, "b": {}}, map[string]int{"a": 1, "b": tc.copies})
			if r.Passed != tc.pass || (err == nil) != tc.pass {
				t.Fatalf("incorrect success claim: %+v, %v", r, err)
			}
		})
	}
	// A successful API ID absent from SQL must fail even if totals match.
	r := report{Requested: 2, APIAccepted: 2}
	if r.verify([]message{{id: "a", status: "delivered"}, {id: "b", status: "delivered"}},
		map[string]struct{}{"a": {}, "unexpected": {}}, map[string]int{"a": 1, "b": 1}) == nil {
		t.Fatal("inconsistent API/SQL identities accepted")
	}
	r = report{Requested: 2, APIAccepted: 2}
	if r.verify([]message{{id: "a", status: "delivered"}, {id: "b", status: "delivered"}},
		map[string]struct{}{"a": {}}, map[string]int{"a": 1, "b": 1}) == nil {
		t.Fatal("duplicate API identity concealed an unacknowledged SQL message")
	}
}

func TestCounterEndpointErrorsCannotBecomeSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"by_message":{"a":1}}`)
			}))
			defer server.Close()
			counts, e := sinkCounts(context.Background(), server.Client(), server.URL)
			if status == http.StatusOK && (e != nil || counts["a"] != 1) {
				t.Fatalf("lost counters: %v, %v", counts, e)
			}
			if status != http.StatusOK && e == nil {
				t.Fatal("unavailable counter source accepted")
			}
		})
	}
}
