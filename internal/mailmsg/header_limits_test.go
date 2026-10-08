package mailmsg

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

func TestRawFoldedReferencesRemainCompliant(t *testing.T) {
	m := fixture()
	m.Payload.Raw = []byte("From: hello@example.test\r\nSubject: folded\r\nReferences: " + strings.Repeat("<prior-message@example.net>\r\n ", 80) + "\r\n\r\nbody\r\n")
	before, e := mail.ReadMessage(bytes.NewReader(m.Payload.Raw))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := Build(m, "mail.example.test")
	if e != nil {
		t.Fatal(e)
	}
	if e = ValidateLines(raw); e != nil {
		t.Fatal(e)
	}
	after, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil || strings.Join(strings.Fields(before.Header.Get("References")), " ") != strings.Join(strings.Fields(after.Header.Get("References")), " ") {
		t.Fatalf("References changed: %v", e)
	}
	m.Payload.Raw = []byte("From: hello@example.test\r\nSubject: folded\r\nX-Unbreakable: " + strings.Repeat("x", 2000) + "\r\n\r\nbody\r\n")
	if _, e = Build(m, "mail.example.test"); e == nil {
		t.Fatal("unbreakable header accepted")
	}
}

func TestAttachmentMediaTypeIsBounded(t *testing.T) {
	m := fixture()
	m.Payload.Attachments[0].ContentType = "application/octet-stream; name=\"" + strings.Repeat("x", 2000) + "\""
	if _, e := Build(m, "mail.example.test"); e == nil {
		t.Fatal("oversized attachment header accepted")
	}
}
