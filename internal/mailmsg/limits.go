package mailmsg

import (
	"bytes"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
)

const MaxWireBytes = 10 * 1024 * 1024
const signatureReserve = 4096

// Preflight uses the longest envelope address and maximum legal hostname. UUIDs,
// MIME boundaries and supported RSA DKIM headers have bounded wire lengths.
func ValidateWire(r domain.SendRequest, limit int) error {
	if limit == 0 {
		limit = MaxWireBytes
	}
	recipient := ""
	for _, v := range r.To {
		if len(v) > len(recipient) {
			recipient = v
		}
	}
	m := domain.Message{ID: strings.Repeat("0", 36), AttemptID: strings.Repeat("0", 36), From: r.From, Recipient: recipient, CreatedAt: time.Unix(0, 0).UTC(), Payload: r}
	raw, e := Build(m, strings.Repeat("a.", 125)+"aaa")
	if e != nil {
		return e
	}
	if len(raw)+signatureReserve > limit {
		return domain.Invalid("message_size", "encoded MIME with DKIM exceeds configured wire budget")
	}
	return ValidateLines(raw)
}

// SMTP's physical line budget counts octets without the CRLF terminator.
func ValidateLines(raw []byte) error {
	for len(raw) > 0 {
		n := bytes.IndexByte(raw, '\n')
		if n < 0 {
			n = len(raw)
		}
		line := raw[:n]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) > 998 {
			return domain.Invalid("mime", "SMTP line exceeds 998 bytes; encode or fold the message")
		}
		if n == len(raw) {
			break
		}
		raw = raw[n+1:]
	}
	return nil
}
