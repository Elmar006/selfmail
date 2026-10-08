package mailmsg

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/emersion/go-msgauth/dkim"
)

func GenerateKey() ([]byte, string, error) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		return nil, "", e
	}
	pub, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		return nil, "", e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), base64.StdEncoding.EncodeToString(pub), nil
}
func ParseSigner(b []byte) (crypto.Signer, error) {
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, fmt.Errorf("invalid key PEM")
	}
	k, e := x509.ParsePKCS1PrivateKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	if k.N.BitLen() < 2048 || k.N.BitLen() > 4096 {
		return nil, fmt.Errorf("DKIM RSA key must be 2048..4096 bits")
	}
	return k, nil
}
func MessageID(m domain.Message, host string) string {
	return "<" + m.ID + "." + m.AttemptID + "@" + host + ">"
}
func qp(s string) []byte {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}
func subjectHeader(s string) string {
	if s == "" {
		return ""
	}
	var words []string
	for len(s) > 0 {
		n := min(len(s), 45)
		for n < len(s) && n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		if n == 0 {
			n = 1
		}
		words = append(words, "=?utf-8?B?"+base64.StdEncoding.EncodeToString([]byte(s[:n]))+"?=")
		s = s[n:]
	}
	return strings.Join(words, "\r\n ")
}
func textPart(w *multipart.Writer, typ, body string) error {
	encoding, encoded := encodeText(body)
	p, e := w.CreatePart(textproto.MIMEHeader{"Content-Type": {typ + "; charset=utf-8"}, "Content-Transfer-Encoding": {encoding}})
	if e != nil {
		return e
	}
	_, e = p.Write(encoded)
	return e
}
func encodeText(s string) (string, []byte) {
	escaped := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 127 || c == '=' || (c < 32 && c != '\t' && c != '\r' && c != '\n') {
			escaped++
		}
	}
	if escaped <= len(s)/6 {
		return "quoted-printable", qp(s)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(s))
	var b bytes.Buffer
	for len(encoded) > 0 {
		n := min(len(encoded), 76)
		b.WriteString(encoded[:n])
		b.WriteString("\r\n")
		encoded = encoded[n:]
	}
	return "base64", b.Bytes()
}
func content(r domain.SendRequest) (string, []byte, string, error) {
	if r.HTML == "" {
		enc, b := encodeText(r.Text)
		return "text/plain; charset=utf-8", b, enc, nil
	}
	if r.Text == "" {
		enc, b := encodeText(r.HTML)
		return "text/html; charset=utf-8", b, enc, nil
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if e := textPart(w, "text/plain", r.Text); e != nil {
		return "", nil, "", e
	}
	if e := textPart(w, "text/html", r.HTML); e != nil {
		return "", nil, "", e
	}
	if e := w.Close(); e != nil {
		return "", nil, "", e
	}
	return "multipart/alternative; boundary=" + w.Boundary(), b.Bytes(), "", nil
}
func Build(m domain.Message, host string) ([]byte, error) {
	if len(m.Payload.Raw) > 0 {
		return sanitizeRaw(m, host)
	}
	r := m.Payload
	var body []byte
	typ, body, encoding, e := content(r)
	if e != nil {
		return nil, e
	}
	if strings.HasPrefix(typ, "multipart/") {
		encoding = ""
	}
	if len(r.Attachments) > 0 {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		h := textproto.MIMEHeader{"Content-Type": {typ}}
		if encoding != "" {
			h.Set("Content-Transfer-Encoding", encoding)
		}
		p, e := w.CreatePart(h)
		if e != nil {
			return nil, e
		}
		if _, e = p.Write(body); e != nil {
			return nil, e
		}
		for _, a := range r.Attachments {
			at := a.ContentType
			if len(at) > 255 {
				return nil, domain.Invalid("attachment.content_type", "maximum 255 UTF-8 bytes")
			}
			if at == "" {
				at = "application/octet-stream"
			}
			if _, _, e = mime.ParseMediaType(at); e != nil {
				return nil, domain.Invalid("attachment.content_type", "invalid media type")
			}
			p, e = w.CreatePart(textproto.MIMEHeader{"Content-Type": {at}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename})}, "Content-Transfer-Encoding": {"base64"}})
			if e != nil {
				return nil, e
			}
			encoded := base64.StdEncoding.EncodeToString(a.Data)
			for len(encoded) > 0 {
				n := min(len(encoded), 76)
				if _, e = io.WriteString(p, encoded[:n]+"\r\n"); e != nil {
					return nil, e
				}
				encoded = encoded[n:]
			}
		}
		if e = w.Close(); e != nil {
			return nil, e
		}
		typ = "multipart/mixed; boundary=" + w.Boundary()
		body = b.Bytes()
		encoding = ""
	}
	var out bytes.Buffer
	from := (&mail.Address{Name: r.FromName, Address: m.From}).String()
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s\r\n", from, m.Recipient, subjectHeader(r.Subject), m.CreatedAt.Format(time.RFC1123Z), MessageID(m, host), typ)
	if r.ReplyTo != "" {
		fmt.Fprintf(&out, "Reply-To: %s\r\n", r.ReplyTo)
	}
	if encoding != "" {
		fmt.Fprintf(&out, "Content-Transfer-Encoding: %s\r\n", encoding)
	}
	out.WriteString("\r\n")
	out.Write(body)
	out.WriteString("\r\n")
	return out.Bytes(), nil
}

// Raw SMTP messages retain MIME structure; caller-controlled delivery headers are replaced.
func ValidateRaw(raw []byte, envelope string) (string, error) {
	m, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		return "", domain.Invalid("mime", "invalid headers")
	}
	if len(m.Header["From"]) != 1 || len(m.Header["Subject"]) > 1 {
		return "", domain.Invalid("mime", "ambiguous From or Subject")
	}
	from, e := domain.Address(m.Header.Get("From"))
	if e != nil || from != envelope {
		return "", domain.Invalid("mime", "From must match MAIL FROM")
	}
	if m.Header.Get("Sender") != "" {
		sender, e := domain.Address(m.Header.Get("Sender"))
		if e != nil || sender != from {
			return "", domain.Invalid("mime", "Sender must match From")
		}
	}
	for _, values := range m.Header {
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n\x00") {
				return "", domain.Invalid("mime", "invalid header")
			}
		}
	}
	subject, e := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if e != nil {
		return "", domain.Invalid("subject", "invalid encoded subject")
	}
	if len(subject) > 998 {
		return "", domain.Invalid("subject", "subject too long")
	}
	return subject, nil
}
func sanitizeRaw(m domain.Message, host string) ([]byte, error) {
	r, e := mail.ReadMessage(bytes.NewReader(m.Payload.Raw))
	if e != nil {
		return nil, e
	}
	var out bytes.Buffer
	drop := map[string]bool{"Bcc": true, "Return-Path": true, "Received": true, "Dkim-Signature": true, "Authentication-Results": true, "Message-Id": true, "Date": true, "To": true}
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if drop[k] || strings.HasPrefix(strings.ToLower(k), "x-selfmail-") {
			continue
		}
		for _, v := range r.Header[k] {
			if e = writeHeader(&out, k, v); e != nil {
				return nil, e
			}
		}
	}
	fmt.Fprintf(&out, "To: %s\r\nDate: %s\r\nMessage-ID: %s\r\n\r\n", m.Recipient, m.CreatedAt.Format(time.RFC1123Z), MessageID(m, host))
	if _, e = io.Copy(&out, r.Body); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}

func writeHeader(out *bytes.Buffer, name, value string) error {
	line := name + ": " + value
	first := true
	for len(line) > 998 {
		cut := strings.LastIndexAny(line[:998], " \t")
		if cut < 1 || (first && cut <= len(name)+1) {
			return domain.Invalid("mime", "header contains an unbreakable line over 998 bytes")
		}
		out.WriteString(line[:cut] + "\r\n")
		line = line[cut:]
		first = false
	}
	out.WriteString(line + "\r\n")
	return nil
}
func Sign(raw []byte, d domain.Domain, key []byte) ([]byte, error) {
	signer, e := ParseSigner(key)
	if e != nil {
		return nil, e
	}
	var b bytes.Buffer
	opts := dkim.SignOptions{Domain: d.Name, Selector: d.Selector, Signer: signer, HeaderCanonicalization: dkim.CanonicalizationRelaxed, BodyCanonicalization: dkim.CanonicalizationRelaxed, HeaderKeys: []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type", "Content-Transfer-Encoding", "Reply-To"}}
	if e = dkim.Sign(&b, bytes.NewReader(raw), &opts); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
