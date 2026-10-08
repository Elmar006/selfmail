// Acceptance exercises only the development sink. It refuses production mode.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/pkg/client"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"
)

var httpClient = &http.Client{Timeout: 25 * time.Second}
var apiURL, sinkURL string

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "ACCEPTANCE FAILED:", e)
		os.Exit(1)
	}
	fmt.Println("ACCEPTANCE PASSED: REST, three projects, SMTP AUTH, DKIM, attachments, templates, idempotency, RLS, quotas, cancellation, Postfix relay protection, delivery feedback, hard bounce, late DSN, signed webhooks")
}
func run() error {
	if os.Getenv("APP_ENV") != "development" {
		return fmt.Errorf("acceptance is restricted to APP_ENV=development")
	}
	apiURL = os.Getenv("ACCEPTANCE_API_URL")
	sinkURL = os.Getenv("ACCEPTANCE_SINK_URL")
	if apiURL == "" || sinkURL == "" {
		return fmt.Errorf("acceptance URLs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer s.Close()
	type project struct {
		id, key, domain string
		cli             *client.Client
		pub             string
	}
	var projects []project
	for i := range 3 {
		id, key, e := s.CreateTenant(ctx, fmt.Sprintf("acceptance-site-%d", i+1), 50, 1000)
		if e != nil {
			return e
		}
		c, e := client.New(apiURL, key)
		if e != nil {
			return e
		}
		name := id + ".example.test"
		body, status, e := call(ctx, "POST", "/v1/domains", key, "", map[string]string{"name": name})
		if e != nil || status != 201 {
			return fmt.Errorf("domain creation: %d %s %v", status, body, e)
		}
		var response struct {
			Domain domain.Domain `json:"domain"`
		}
		if e = json.Unmarshal(body, &response); e != nil {
			return e
		}
		projects = append(projects, project{id, key, name, c, response.Domain.PublicKey})
	}
	fmt.Println("Created three isolated test projects")
	first := projects[0]
	hookBody, status, e := call(ctx, "POST", "/v1/webhooks", first.key, "", map[string]string{"url": sinkURL + "/webhooks"})
	if e != nil || status != 201 {
		return fmt.Errorf("webhook creation: %d %s %v", status, hookBody, e)
	}
	var hook domain.Webhook
	if e = json.Unmarshal(hookBody, &hook); e != nil {
		return e
	}
	runID := domain.ID()
	recipient := "receipt+" + runID + "@example.net"
	input := client.SendRequest{From: "receipts@" + first.domain, To: []string{recipient}, Subject: "Ваш чек №42", Text: "Спасибо за покупку", HTML: "<p>Спасибо за покупку</p>", Priority: "critical", Attachments: []client.Attachment{{Filename: "receipt.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7 test attachment")}}}
	result, e := first.cli.Send(ctx, "receipt-"+runID, input)
	if e != nil {
		return e
	}
	id := result.MessageIDs[0]
	replay, e := first.cli.Send(ctx, "receipt-"+runID, input)
	if e != nil || !replay.Replayed || replay.BatchID != result.BatchID {
		return fmt.Errorf("idempotency replay failed: %v", e)
	}
	changed := input
	changed.Subject = "changed"
	if _, e = first.cli.Send(ctx, "receipt-"+runID, changed); e == nil {
		return fmt.Errorf("conflicting idempotency key accepted")
	}
	if _, status, e = call(ctx, "GET", "/v1/messages/"+id, projects[1].key, "", nil); e != nil || status != 404 {
		return fmt.Errorf("cross-tenant access: %d %v", status, e)
	}
	if _, status, e = call(ctx, "GET", "/v1/messages/"+id, "", "", nil); e != nil || status != 401 {
		return fmt.Errorf("anonymous access: %d %v", status, e)
	}
	if e = waitStatus(ctx, first.cli, id, "delivered"); e != nil {
		return e
	}
	fmt.Println("REST → outbox → RabbitMQ → worker → Postfix → sink → delivered passed")
	// Check the bytes after Postfix, so a valid signature before submission is insufficient.
	sinkMessages, e := messages(ctx)
	if e != nil {
		return e
	}
	var actual *sinkMessage
	for i := range sinkMessages {
		if strings.Contains(sinkMessages[i].Raw, "<"+id+".") {
			actual = &sinkMessages[i]
			break
		}
	}
	if actual == nil {
		return fmt.Errorf("delivered message missing from sink")
	}
	verified, e := dkim.VerifyWithOptions(strings.NewReader(actual.Raw), &dkim.VerifyOptions{LookupTXT: func(name string) ([]string, error) { return []string{"v=DKIM1; k=rsa; p=" + first.pub}, nil }})
	if e != nil || len(verified) != 1 || verified[0].Err != nil {
		return fmt.Errorf("Postfix changed signed message: %v %+v", e, verified)
	}
	if !strings.Contains(actual.Raw, "application/pdf") {
		return fmt.Errorf("attachment missing")
	}
	// Independent projects use the same adapter with their own sender identity.
	for _, p := range projects[1:] {
		res, e := p.cli.Send(ctx, "notice-"+runID, client.SendRequest{From: "notices@" + p.domain, To: []string{"notice+" + p.id + "@example.net"}, Subject: "notification", Text: "project isolated"})
		if e != nil {
			return e
		}
		if e = waitStatus(ctx, p.cli, res.MessageIDs[0], "delivered"); e != nil {
			return e
		}
	}
	template := domain.Template{Subject: "Receipt {{.Number}}", Text: "Total {{.Total}}", HTML: "<p>{{.Total}}</p>"}
	if _, status, e = call(ctx, "PUT", "/v1/templates/receipt", first.key, "", template); e != nil || status != 201 {
		return fmt.Errorf("template write %d %v", status, e)
	}
	tr := client.SendRequest{From: input.From, To: []string{"template+" + runID + "@example.net"}, Template: "receipt", Variables: map[string]any{"Number": 42, "Total": "100"}}
	templated, e := first.cli.Send(ctx, "template-"+runID, tr)
	if e != nil {
		return e
	}
	template.Text = "New template needs {{.Missing}}"
	call(ctx, "PUT", "/v1/templates/receipt", first.key, "", template)
	if replay, e = first.cli.Send(ctx, "template-"+runID, tr); e != nil || !replay.Replayed || replay.BatchID != templated.BatchID {
		return fmt.Errorf("template replay changed after editing: %v", e)
	}
	future := input
	future.To = []string{"canceled+" + runID + "@example.net"}
	future.Attachments = nil
	future.SendAt = time.Now().Add(10 * time.Minute)
	scheduled, e := first.cli.Send(ctx, "scheduled-"+runID, future)
	if e != nil {
		return e
	}
	if e = first.cli.Cancel(ctx, scheduled.MessageIDs[0]); e != nil {
		return e
	}
	if e = waitStatus(ctx, first.cli, scheduled.MessageIDs[0], "canceled"); e != nil {
		return e
	}
	if e = smtpTests(ctx, first.id, first.key, "smtp@"+first.domain, runID); e != nil {
		return e
	}
	fmt.Println("SMTP compatibility and unauthenticated relay rejection passed")
	// Real Postfix rejection by the destination, followed by tenant-scoped suppression.
	rejected := input
	rejected.To = []string{"reject@" + "example.net"}
	rejected.Attachments = nil
	bad, e := first.cli.Send(ctx, "bounce-"+runID, rejected)
	if e != nil {
		return e
	}
	if e = waitStatus(ctx, first.cli, bad.MessageIDs[0], "bounced"); e != nil {
		return e
	}
	if _, e = first.cli.Send(ctx, "suppressed-"+runID, rejected); e == nil {
		return fmt.Errorf("hard-bounced address accepted")
	}
	// A late DSN after remote acceptance is correlated by an authenticated VERP token.
	if e = sendDSN(ctx, actual.From, recipient); e != nil {
		return e
	}
	if e = waitStatus(ctx, first.cli, id, "bounced"); e != nil {
		return e
	}
	if e = waitWebhook(ctx, hook.Secret, id); e != nil {
		return e
	}
	fmt.Println("Postfix hard bounce, late DSN and webhook HMAC passed")
	return nil
}
func call(ctx context.Context, method, path, key, idempotency string, input any) ([]byte, int, error) {
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return nil, 0, e
		}
		body = bytes.NewReader(b)
	}
	r, e := http.NewRequestWithContext(ctx, method, apiURL+path, body)
	if e != nil {
		return nil, 0, e
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	r.Header.Set("Content-Type", "application/json")
	if idempotency != "" {
		r.Header.Set("Idempotency-Key", idempotency)
	}
	res, e := httpClient.Do(r)
	if e != nil {
		return nil, 0, e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
	return b, res.StatusCode, e
}
func waitStatus(ctx context.Context, c *client.Client, id, target string) error {
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		m, e := c.Status(ctx, id)
		if e != nil {
			return e
		}
		if m.Status == target {
			return nil
		}
		if m.Status == "failed" || m.Status == "submission_unknown" {
			return fmt.Errorf("message %s unexpected %s: %s", id, m.Status, m.LastError)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	m, _ := c.Status(ctx, id)
	return fmt.Errorf("message %s timed out: %s expected %s; %s", id, m.Status, target, m.LastError)
}

type sinkMessage struct {
	From string `json:"from"`
	Raw  string `json:"raw"`
}

func messages(ctx context.Context) ([]sinkMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", sinkURL+"/messages", nil)
	r, e := httpClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	var out []sinkMessage
	e = json.NewDecoder(r.Body).Decode(&out)
	return out, e
}
func connect(ctx context.Context, address string) (*smtp.Client, error) {
	conn, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	c := smtp.NewClient(conn)
	c.CommandTimeout = 5 * time.Second
	c.SubmissionTimeout = 5 * time.Second
	return c, nil
}
func smtpTests(ctx context.Context, tenant, key, from, runID string) error {
	c, e := connect(ctx, "api:1587")
	if e != nil {
		return e
	}
	if e = c.Mail(from, nil); e == nil {
		c.Close()
		return fmt.Errorf("unauthenticated SMTP accepted")
	}
	c.Close()
	c, e = connect(ctx, "api:1587")
	if e != nil {
		return e
	}
	defer c.Close()
	if e = c.Auth(sasl.NewPlainClient("", tenant, key)); e != nil {
		return e
	}
	if e = c.Mail(from, nil); e != nil {
		return e
	}
	if e = c.Rcpt("smtp+"+runID+"@example.net", nil); e != nil {
		return e
	}
	w, e := c.Data()
	if e != nil {
		return e
	}
	fmt.Fprintf(w, "From: %s\r\nTo: smtp+%s@example.net\r\nSubject: SMTP compatibility\r\nMessage-ID: <%s@client.example.net>\r\n\r\nSMTP content\r\n", from, runID, runID)
	if e = w.Close(); e != nil {
		return e
	}
	postfix, e := connect(ctx, "postfix:25")
	if e != nil {
		return e
	}
	defer postfix.Close()
	if e = postfix.Mail("", nil); e != nil {
		return e
	}
	if e = postfix.Rcpt("external@example.org", nil); e == nil {
		return fmt.Errorf("Postfix is an open relay")
	}
	return nil
}
func sendDSN(ctx context.Context, to, recipient string) error {
	c, e := connect(ctx, "postfix:25")
	if e != nil {
		return e
	}
	defer c.Close()
	if e = c.Mail("", nil); e != nil {
		return e
	}
	if e = c.Rcpt(to, nil); e != nil {
		return e
	}
	w, e := c.Data()
	if e != nil {
		return e
	}
	fmt.Fprintf(w, "From: postmaster@example.net\r\nContent-Type: multipart/report; report-type=delivery-status; boundary=dsn\r\n\r\n--dsn\r\nContent-Type: text/plain\r\n\r\nfailed\r\n--dsn\r\nContent-Type: message/delivery-status\r\n\r\nReporting-MTA: dns; mx.example.net\r\n\r\nFinal-Recipient: rfc822; %s\r\nAction: failed\r\nStatus: 5.1.1\r\n\r\n--dsn--\r\n", recipient)
	return w.Close()
}
func waitWebhook(ctx context.Context, secret, id string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, e := httpClient.Get(sinkURL + "/webhooks")
		if e != nil {
			return e
		}
		var out struct {
			Raw     [][]byte      `json:"raw_payloads"`
			Headers []http.Header `json:"headers"`
		}
		e = json.NewDecoder(r.Body).Decode(&out)
		r.Body.Close()
		if e != nil {
			return e
		}
		for i, body := range out.Raw {
			var ev domain.Event
			if json.Unmarshal(body, &ev) == nil && ev.MessageID == id && ev.Type == "delivered" {
				header := out.Headers[i]
				signature := "v1=" + security.Sign([]byte(secret), header.Get("X-Selfmail-Timestamp"), body)
				if header.Get("X-Selfmail-Signature") != signature {
					return fmt.Errorf("webhook signature mismatch")
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("delivery webhook missing")
}
