// benchmark measures a finite, unpaced, concurrent development workload.
// It reports API acceptance separately from confirmed local SMTP delivery.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/pkg/client"
)

type options struct {
	Count, Concurrency, TextBytes, AttachmentBytes, RecipientDomains int
	Timeout                                                          time.Duration
}

func (o options) validate(env, api, sink string) error {
	if env != "development" || api != "http://api:8080" || sink != "http://sink:8025" {
		return fmt.Errorf("benchmark requires development mode and isolated Compose API/sink URLs")
	}
	if o.Count < 2 || o.Count > 5000 || o.Concurrency < 1 || o.Concurrency > 64 || o.Concurrency > o.Count ||
		o.TextBytes < 1 || o.TextBytes > 64*1024 || o.AttachmentBytes < 0 || o.AttachmentBytes > 2*1024*1024 ||
		o.RecipientDomains < 1 || o.RecipientDomains > 32 || o.Timeout < time.Second || o.Timeout > 30*time.Minute {
		return fmt.Errorf("bounds: count 2..5000, concurrency 1..64 (<=count), text 1..65536 bytes, attachment 0..2097152 bytes, recipient-domains 1..32, timeout 1s..30m")
	}
	// Leave room for metadata below the service's 512 MiB per-tenant payload cap.
	if int64((o.Count+2)/3)*int64(o.TextBytes+4*((o.AttachmentBytes+2)/3)+4096) > 448*1024*1024 {
		return fmt.Errorf("workload exceeds the benchmark's safe per-tenant payload budget; reduce count/payload")
	}
	return nil
}

type distribution struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

func summarize(values []time.Duration) distribution {
	if len(values) == 0 {
		return distribution{}
	}
	values = append([]time.Duration(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(p float64) float64 {
		return float64(values[int(math.Ceil(p*float64(len(values))))-1]) / float64(time.Millisecond)
	}
	return distribution{percentile(.5), percentile(.95), percentile(.99), percentile(1)}
}

type report struct {
	RunID                  string         `json:"run_id"`
	StartedAt              time.Time      `json:"started_at"`
	Requested              int            `json:"requested"`
	Concurrency            int            `json:"concurrency"`
	TextBytes              int            `json:"text_bytes"`
	AttachmentBytes        int            `json:"attachment_bytes"`
	RecipientDomains       int            `json:"recipient_domains"`
	APIAccepted            int            `json:"api_accepted"`
	APIErrors              map[string]int `json:"api_errors"`
	SQLAccepted            int            `json:"sql_accepted"`
	Statuses               map[string]int `json:"statuses"`
	Delivered              int            `json:"delivered"`
	Copies                 int            `json:"sink_copies"`
	Missing                int            `json:"missing_sink_messages"`
	DuplicateCopies        int            `json:"duplicate_sink_copies"`
	AcceptanceSeconds      float64        `json:"acceptance_seconds"`
	ConfirmationSeconds    float64        `json:"confirmation_seconds"`
	DrainSeconds           float64        `json:"drain_seconds"`
	AcceptancePerSecond    float64        `json:"api_accepted_per_second"`
	EndToEndPerSecond      float64        `json:"confirmed_end_to_end_per_second"`
	AcceptanceLatency      distribution   `json:"successful_api_latency"`
	SQLConfirmationLatency distribution   `json:"sql_confirmation_latency"`
	Passed                 bool           `json:"passed"`
}

type message struct {
	id, status       string
	created, updated time.Time
}

func (r *report) verify(messages []message, successes map[string]struct{}, counts map[string]int) error {
	r.SQLAccepted = len(messages)
	r.Statuses = map[string]int{}
	identitiesMatch := len(successes) == r.APIAccepted
	var latencies []time.Duration
	for _, m := range messages {
		r.Statuses[m.status]++
		if m.status == "delivered" {
			r.Delivered++
			latencies = append(latencies, m.updated.Sub(m.created))
		}
		copies := counts[m.id]
		r.Copies += copies
		if copies == 0 {
			r.Missing++
		}
		if copies > 1 {
			r.DuplicateCopies += copies - 1
		}
		if _, ok := successes[m.id]; !ok {
			identitiesMatch = false
		}
		delete(successes, m.id)
	}
	r.SQLConfirmationLatency = summarize(latencies)
	r.Passed = len(r.APIErrors) == 0 && r.APIAccepted == r.Requested && r.SQLAccepted == r.Requested &&
		r.Delivered == r.Requested && r.Missing == 0 && r.DuplicateCopies == 0 && identitiesMatch && len(successes) == 0
	if !r.Passed {
		return fmt.Errorf("workload failed: inspect API errors, SQL outcomes and SMTP copy counts in the report")
	}
	return nil
}

func main() {
	o := options{}
	flag.IntVar(&o.Count, "count", 120, "total requests; one recipient per request")
	flag.IntVar(&o.Concurrency, "concurrency", 2, "unpaced concurrent API clients; stock ingress work budget is two")
	flag.IntVar(&o.TextBytes, "text-bytes", 1024, "ASCII body bytes with bounded lines")
	flag.IntVar(&o.AttachmentBytes, "attachment-bytes", 0, "attachment bytes per request")
	flag.IntVar(&o.RecipientDomains, "recipient-domains", 1, "distinct example.test destination domains")
	flag.DurationVar(&o.Timeout, "timeout", 10*time.Minute, "workload and drain deadline")
	flag.Parse()
	r, e := run(context.Background(), o)
	if r.RunID != "" {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(r); encodeErr != nil && e == nil {
			e = encodeErr
		}
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "BENCHMARK FAILED:", e)
		os.Exit(1)
	}
}

func run(parent context.Context, o options) (report, error) {
	api, sink := os.Getenv("ACCEPTANCE_API_URL"), os.Getenv("ACCEPTANCE_SINK_URL")
	if e := o.validate(os.Getenv("APP_ENV"), api, sink); e != nil {
		return report{}, e
	}
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	s, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return report{}, e
	}
	defer s.Close()
	transport := &http.Transport{MaxIdleConns: o.Concurrency + 4, MaxIdleConnsPerHost: o.Concurrency, IdleConnTimeout: time.Minute}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Timeout: 25 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	counts, e := sinkCounts(ctx, httpClient, sink)
	if e != nil {
		return report{}, e
	}
	if len(counts)+o.Count > 10000 {
		return report{}, fmt.Errorf("sink's 10000-message counter capacity would be exceeded; use a fresh isolated sink")
	}
	type project struct {
		cli  *client.Client
		from string
	}
	projects := make([]project, 0, 3)
	tenants := make([]string, 0, 3)
	for range 3 {
		id, key, err := s.CreateTenant(ctx, "benchmark-"+domain.ID(), 10000, o.Count+1)
		if err != nil {
			return report{}, err
		}
		cli, err := client.New(api, key)
		if err != nil {
			return report{}, err
		}
		cli.HTTP = httpClient
		body, _ := json.Marshal(map[string]string{"name": id + ".example.test"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/v1/domains", strings.NewReader(string(body)))
		if err != nil {
			return report{}, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			return report{}, err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			return report{}, fmt.Errorf("benchmark domain creation returned HTTP %d", resp.StatusCode)
		}
		projects = append(projects, project{cli, "notify@" + id + ".example.test"})
		tenants = append(tenants, id)
	}
	r := report{RunID: domain.ID(), Requested: o.Count, Concurrency: o.Concurrency, TextBytes: o.TextBytes,
		AttachmentBytes: o.AttachmentBytes, RecipientDomains: o.RecipientDomains, APIErrors: map[string]int{}}
	text := strings.Repeat("notification\n", (o.TextBytes+12)/13)[:o.TextBytes]
	var attachments []client.Attachment
	if o.AttachmentBytes > 0 {
		// Random bytes avoid inflating results through PostgreSQL TOAST compression
		// of a repeated-character fixture. This is synthetic data, not a real PDF.
		data := make([]byte, o.AttachmentBytes)
		if _, e = rand.Read(data); e != nil {
			return report{}, e
		}
		attachments = []client.Attachment{{Filename: "receipt.bin", ContentType: "application/octet-stream", Data: data}}
	}
	type result struct {
		id      string
		latency time.Duration
		err     error
	}
	results := make([]result, o.Count)
	var next, completed atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	r.StartedAt = start.UTC()
	for range o.Concurrency {
		wg.Go(func() {
			for {
				n := int(next.Add(1) - 1)
				if n >= o.Count {
					return
				}
				p := projects[n%3]
				input := client.SendRequest{From: p.from, To: []string{fmt.Sprintf("bench-%d@destination-%d.example.test", n, n%o.RecipientDomains)},
					Subject: "transactional benchmark", Text: text, Priority: []string{"critical", "normal", "bulk"}[n%3], Attachments: attachments}
				before := time.Now()
				out, err := p.cli.Send(ctx, fmt.Sprintf("bench-%s-%d", r.RunID, n), input)
				results[n] = result{latency: time.Since(before), err: err}
				if err == nil {
					if len(out.MessageIDs) != 1 || !domain.ValidID(out.MessageIDs[0]) || out.Replayed {
						results[n].err = fmt.Errorf("unexpected send result")
					} else {
						results[n].id = out.MessageIDs[0]
					}
				}
				completed.Add(1)
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	progress := time.NewTicker(10 * time.Second)
	defer progress.Stop()
submit:
	for {
		select {
		case <-done:
			break submit
		case <-progress.C:
			fmt.Fprintf(os.Stderr, "benchmark submitting completed=%d/%d elapsed=%s\n", completed.Load(), o.Count, time.Since(start).Round(time.Second))
		}
	}
	r.AcceptanceSeconds = time.Since(start).Seconds()
	successes := map[string]struct{}{}
	var latencies []time.Duration
	for _, res := range results {
		if res.err != nil {
			category := "transport_or_contract"
			var apiErr *client.APIError
			if errors.As(res.err, &apiErr) {
				category = fmt.Sprintf("http_%d", apiErr.Status)
			}
			r.APIErrors[category]++
			continue
		}
		r.APIAccepted++
		successes[res.id] = struct{}{}
		latencies = append(latencies, res.latency)
	}
	r.AcceptancePerSecond = float64(r.APIAccepted) / r.AcceptanceSeconds
	r.AcceptanceLatency = summarize(latencies)
	// SQL verification includes all messages from the new tenants, even if an
	// HTTP response was lost. Failed/ambiguous requests are never silently retried.
	var messages []message
	for {
		messages, e = readMessages(ctx, s, tenants)
		if e != nil {
			return r, e
		}
		pending, delivered := 0, 0
		for _, m := range messages {
			switch m.status {
			case "delivered":
				delivered++
			case "failed", "bounced", "canceled", "suppressed", "submission_unknown":
			default:
				pending++
			}
		}
		if pending == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-progress.C:
			fmt.Fprintf(os.Stderr, "benchmark draining delivered=%d pending=%d elapsed=%s\n", delivered, pending, time.Since(start).Round(time.Second))
		case <-time.After(time.Second):
		}
	}
	r.ConfirmationSeconds = time.Since(start).Seconds()
	r.DrainSeconds = r.ConfirmationSeconds - r.AcceptanceSeconds
	counts, e = sinkCounts(ctx, httpClient, sink)
	if e != nil {
		return r, e
	}
	e = r.verify(messages, successes, counts)
	r.EndToEndPerSecond = float64(r.Delivered) / r.ConfirmationSeconds
	return r, e
}

func readMessages(ctx context.Context, s *store.Store, tenants []string) ([]message, error) {
	rows, e := s.Pool.Query(ctx, "SELECT id::text,status,created_at,updated_at FROM messages WHERE tenant_id=ANY($1::uuid[])", tenants)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var messages []message
	for rows.Next() {
		var m message
		if e = rows.Scan(&m.id, &m.status, &m.created, &m.updated); e != nil {
			return nil, e
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func sinkCounts(ctx context.Context, httpClient *http.Client, sink string) (map[string]int, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, sink+"/counts", nil)
	if e != nil {
		return nil, e
	}
	resp, e := httpClient.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sink counters returned HTTP %d", resp.StatusCode)
	}
	var counts struct {
		ByMessage map[string]int `json:"by_message"`
	}
	e = json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&counts)
	return counts.ByMessage, e
}
