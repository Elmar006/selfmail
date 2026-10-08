// loadtest exercises an isolated development sink; it never targets Internet mail.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/pkg/client"
)

func main() {
	duration := flag.Duration("duration", 30*time.Minute, "steady load duration")
	interval := flag.Duration("interval", 2*time.Second, "acceptance interval")
	flag.Parse()
	if e := run(*duration, *interval); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(duration, interval time.Duration) error {
	if os.Getenv("APP_ENV") != "development" || duration <= 0 || interval < time.Second {
		return fmt.Errorf("development-only controlled load; interval >=1s")
	}
	api, sink := os.Getenv("ACCEPTANCE_API_URL"), os.Getenv("ACCEPTANCE_SINK_URL")
	if api != "http://api:8080" || sink != "http://sink:8025" {
		return fmt.Errorf("isolated Compose URLs required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration+5*time.Minute)
	defer cancel()
	s, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer s.Close()
	type project struct {
		cli  *client.Client
		from string
	}
	var projects []project
	for i := 0; i < 3; i++ {
		id, key, e := s.CreateTenant(ctx, "controlled-load-"+domain.ID(), 50, 10000)
		if e != nil {
			return e
		}
		cli, e := client.New(api, key)
		if e != nil {
			return e
		}
		// Runtime API generates the DKIM identity and persists it through its normal policy.
		body, _ := json.Marshal(map[string]string{"name": id + ".example.test"})
		r, _ := http.NewRequestWithContext(ctx, "POST", api+"/v1/domains", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			return e
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			return fmt.Errorf("domain create %d", resp.StatusCode)
		}
		projects = append(projects, project{cli, "notify@" + id + ".example.test"})
	}
	start := time.Now()
	ids := []string{}
	owners := map[string]*client.Client{}
	var maxAcceptance time.Duration
	for n := 0; time.Since(start) < duration; n++ {
		p := projects[n%len(projects)]
		text := strings.Repeat("notification ", 800)
		input := client.SendRequest{From: p.from, To: []string{fmt.Sprintf("load-%d@example.net", n)}, Subject: "controlled transactional load", Text: text, Priority: []string{"critical", "normal", "bulk"}[n%3]}
		if n%20 == 0 {
			input.Attachments = []client.Attachment{{Filename: "receipt.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.7\n" + strings.Repeat("x", 2*1024*1024))}}
		}
		if n%60 == 0 {
			input.Text = strings.Repeat("界", (5*1024*1024)/3)
			input.Attachments = nil
		}
		before := time.Now()
		result, e := p.cli.Send(ctx, "load-"+domain.ID(), input)
		if e != nil {
			return fmt.Errorf("load send %d: %w", n, e)
		}
		elapsed := time.Since(before)
		maxAcceptance = max(maxAcceptance, elapsed)
		ids = append(ids, result.MessageIDs...)
		for _, id := range result.MessageIDs {
			owners[id] = p.cli
		}
		if n%30 == 0 {
			fmt.Printf("load elapsed=%s accepted=%d max_acceptance_ms=%d\n", time.Since(start).Round(time.Second), len(ids), maxAcceptance.Milliseconds())
		}
		timer := time.NewTimer(max(interval-time.Since(before), time.Millisecond))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	for _, id := range ids {
		for {
			m, e := owners[id].Status(ctx, id)
			if e != nil {
				return e
			}
			if m.Status == "delivered" {
				break
			}
			if m.Status == "failed" || m.Status == "bounced" || m.Status == "submission_unknown" {
				return fmt.Errorf("load message %s ended %s", id, m.Status)
			}
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	resp, e := http.Get(sink + "/counts")
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	var counts struct {
		ByMessage map[string]int `json:"by_message"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&counts); e != nil {
		return e
	}
	for _, id := range ids {
		if counts.ByMessage[id] != 1 {
			return fmt.Errorf("SMTP copies for %s: %d", id, counts.ByMessage[id])
		}
	}
	fmt.Printf("LOAD PASSED duration=%s accepted=%d delivered=%d duplicates=0 max_acceptance_ms=%d\n", time.Since(start).Round(time.Second), len(ids), len(ids), maxAcceptance.Milliseconds())
	return nil
}
