package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAddresses(t *testing.T) {
	for _, input := range []string{"alice@example.net", "Alice <ALICE@example.net>", "user+receipt@sub.example.net"} {
		if _, e := Address(input); e != nil {
			t.Errorf("%q: %v", input, e)
		}
	}
	for _, input := range []string{"a@localhost", "bad\r\nBcc: x@example.net", "a@-bad.example", "а@example.net", "a@exa_mple.net", "<>", strings.Repeat("a", 65) + "@example.net"} {
		if _, e := Address(input); e == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
func TestRequestBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SendRequest)
	}{
		{"duplicates", func(r *SendRequest) { r.To = []string{"a@example.net", "a@EXAMPLE.NET"} }},
		{"header injection", func(r *SendRequest) { r.Subject = "ok\r\nBcc: evil@example.net" }},
		{"missing body", func(r *SendRequest) { r.Text = "" }},
		{"too far", func(r *SendRequest) { r.SendAt = time.Now().Add(31 * 24 * time.Hour) }},
		{"traversal", func(r *SendRequest) { r.Attachments = []Attachment{{Filename: "../secret", Data: []byte("x")}} }},
		{"oversize", func(r *SendRequest) { r.Text = strings.Repeat("x", 5*1024*1024+1) }},
		{"priority", func(r *SendRequest) { r.Priority = "everything" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := SendRequest{From: "a@example.test", To: []string{"a@example.net"}, Text: "ok"}
			tc.mutate(&r)
			if e := Validate(&r); e == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}
func TestIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := ID()
		if !ValidID(id) || seen[id] {
			t.Fatal("invalid or duplicate UUID")
		}
		seen[id] = true
	}
}
