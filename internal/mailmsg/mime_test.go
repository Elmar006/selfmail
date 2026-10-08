package mailmsg

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/emersion/go-msgauth/dkim"
)

func fixture() domain.Message {
	return domain.Message{ID: domain.ID(), AttemptID: domain.ID(), From: "hello@example.test", Recipient: "user@example.net", CreatedAt: time.Now().UTC(), Payload: domain.SendRequest{Subject: "Ваш чек №42", Text: "Спасибо за покупку", HTML: "<b>Спасибо</b>", Attachments: []domain.Attachment{{Filename: "чек.pdf", ContentType: "application/pdf", Data: []byte("%PDF-fixture")}}}}
}
func TestMIMEAndDKIM(t *testing.T) {
	m := fixture()
	raw, e := Build(m, "mail.example.test")
	if e != nil {
		t.Fatal(e)
	}
	msg, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	subject, e := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if e != nil || subject != m.Payload.Subject {
		t.Fatal("subject encoding")
	}
	typ, params, e := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if e != nil || typ != "multipart/mixed" {
		t.Fatal("mixed MIME missing")
	}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	_, e = reader.NextPart()
	if e != nil {
		t.Fatal(e)
	}
	part, e := reader.NextPart()
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
	if e != nil || string(decoded) != "%PDF-fixture" {
		t.Fatal("attachment changed")
	}
	_, p, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if p["filename"] != "чек.pdf" {
		t.Fatal("filename changed")
	}
	key, pub, e := GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	signed, e := Sign(raw, domain.Domain{Name: "example.test", Selector: "test"}, key)
	if e != nil {
		t.Fatal(e)
	}
	opts := &dkim.VerifyOptions{LookupTXT: func(name string) ([]string, error) { return []string{"v=DKIM1; k=rsa; p=" + pub}, nil }}
	results, e := dkim.VerifyWithOptions(bytes.NewReader(signed), opts)
	if e != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("invalid DKIM %v %v", results, e)
	}
	_ = context.Background()
}
func TestRawIdentityAndBcc(t *testing.T) {
	m := fixture()
	m.Payload.Raw = []byte("From: hello@example.test\r\nTo: original@example.net\r\nBcc: secret@example.net\r\nSubject: test\r\nMessage-ID: <caller@evil.net>\r\nReturn-Path: <forged@evil.net>\r\n\r\nbody\r\n")
	if _, e := ValidateRaw(m.Payload.Raw, m.From); e != nil {
		t.Fatal(e)
	}
	raw, e := Build(m, "mail.example.test")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "Bcc:") || strings.Contains(string(raw), "caller@evil.net") || strings.Contains(string(raw), "Return-Path:") {
		t.Fatal("unsafe header retained")
	}
	m.Payload.Raw = []byte("From: someone@evil.net\r\n\r\nbody")
	if _, e = ValidateRaw(m.Payload.Raw, m.From); e == nil {
		t.Fatal("sender spoof accepted")
	}
}

func TestLongUTF8AndASCIISubjectsFoldLosslessly(t *testing.T) {
	for _, subject := range []string{strings.Repeat("я", 400), strings.Repeat("a", 998), strings.Repeat("🙂", 200)} {
		m := fixture()
		m.Payload.Subject = subject
		raw, e := Build(m, "mail.example.test")
		if e != nil {
			t.Fatal(e)
		}
		head := strings.SplitN(string(raw), "\r\n\r\n", 2)[0]
		for _, line := range strings.Split(head, "\r\n") {
			if len(line) > 998 {
				t.Fatalf("oversized header %d", len(line))
			}
		}
		parsed, e := mail.ReadMessage(bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		decoded, e := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
		if e != nil || decoded != subject {
			t.Fatal("subject changed after folding", e)
		}
	}
}
