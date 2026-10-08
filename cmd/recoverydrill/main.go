// recoverydrill prepares and verifies a real SMTP delivery across a separate
// point-in-time restored database. Runtime traffic remains on the original DB.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/pkg/client"
)

type scenario struct {
	Tenant, Key, Message, Batch, Target string
	JournalInstance                     string
	Request                             client.SendRequest
}

func main() {
	mode := flag.String("mode", "prepare", "prepare|deliver|verify")
	file := flag.String("case", ".verification/recovery-case.json", "private scenario file")
	flag.Parse()
	if e := run(*mode, *file); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(mode, file string) error {
	if os.Getenv("APP_ENV") != "development" {
		return fmt.Errorf("development-only drill")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := os.Getenv("DATABASE_URL")
	if mode == "verify" {
		dsn = os.Getenv("RECOVERY_RESTORE_DATABASE_URL")
		if dsn == "" {
			u, e := url.Parse(os.Getenv("DATABASE_URL"))
			if e != nil {
				return e
			}
			u.Host = "restorelab:5432"
			dsn = u.String()
		}
		if !strings.Contains(dsn, "@restorelab:5432/") {
			return fmt.Errorf("verify requires isolated restorelab database")
		}
	}
	s, e := store.Open(ctx, dsn)
	if e != nil {
		return e
	}
	defer s.Close()
	var sc scenario
	api := os.Getenv("ACCEPTANCE_API_URL")
	if api != "http://api:8080" {
		return fmt.Errorf("isolated Compose API required")
	}
	if mode == "prepare" {
		sc.Tenant, sc.Key, e = s.CreateTenant(ctx, "physical-restore-drill", 50, 1000)
		if e != nil {
			return e
		}
		domainName := sc.Tenant + ".example.test"
		body, _ := json.Marshal(map[string]string{"name": domainName})
		r, _ := http.NewRequestWithContext(ctx, "POST", api+"/v1/domains", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+sc.Key)
		r.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			return e
		}
		resp.Body.Close()
		if resp.StatusCode != 201 {
			return fmt.Errorf("domain creation %d", resp.StatusCode)
		}
		sc.Request = client.SendRequest{From: "restore@" + domainName, To: []string{"restore+" + domain.ID() + "@example.net"}, Subject: "physical PITR recovery", Text: "one SMTP copy", SendAt: time.Now().Add(time.Hour)}
		c, _ := client.New(api, sc.Key)
		res, e := c.Send(ctx, "physical-restore-key", sc.Request)
		if e != nil {
			return e
		}
		sc.Batch, sc.Message = res.BatchID, res.MessageIDs[0]
	} else {
		b, e := os.ReadFile(file)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &sc); e != nil {
			return e
		}
		if mode == "deliver" {
			var target time.Time
			if e = s.Pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&target); e != nil {
				return e
			}
			sc.Target = target.UTC().Format("2006-01-02 15:04:05.999999-07:00")
			if _, e = s.Pool.Exec(ctx, "UPDATE messages SET next_attempt_at=clock_timestamp() WHERE id=$1", sc.Message); e != nil {
				return e
			}
			if _, e = s.Pool.Exec(ctx, "UPDATE outbox SET available_at=clock_timestamp() WHERE message_id=$1", sc.Message); e != nil {
				return e
			}
			c, _ := client.New(api, sc.Key)
			for {
				m, e := c.Status(ctx, sc.Message)
				if e != nil {
					return e
				}
				if m.Status == "delivered" {
					break
				}
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			v, e := security.NewVault(os.Getenv("MASTER_KEY"))
			if e != nil {
				return e
			}
			source, e := recovery.New(os.Getenv("CONTROL_DIR"), v)
			if e != nil {
				return e
			}
			state, e := source.Status(ctx)
			if e != nil {
				return e
			}
			sc.JournalInstance = state.Instance
			j, e := journal.New(os.Getenv("JOURNAL_DIR"), v)
			if e != nil {
				return e
			}
			if e = j.Bind(ctx, os.Getenv("CONTROL_DIR"), state.Instance, false); e != nil {
				return e
			}
			if e = j.CopySnapshot(ctx, ".verification/recovery-journal", ".verification/recovery-anchor"); e != nil {
				return e
			}
		} else if mode == "verify" {
			before, e := s.GetMessage(ctx, sc.Tenant, sc.Message)
			if e != nil || before.Status != "queued" {
				return fmt.Errorf("PITR snapshot expected queued: %+v %v", before, e)
			}
			v, e := security.NewVault(os.Getenv("MASTER_KEY"))
			if e != nil {
				return e
			}
			s.Control, e = recovery.New("/tmp/drill-control", v)
			if e != nil {
				return e
			}
			if e = s.Control.Initialize(ctx); e != nil {
				return e
			}
			if e = s.Control.Hold(ctx, "isolated physical PITR verification"); e != nil {
				return e
			}
			s.Journal, e = journal.New(".verification/recovery-journal", v)
			if e != nil {
				return e
			}
			if e = s.Journal.Bind(ctx, ".verification/recovery-anchor", sc.JournalInstance, false); e != nil {
				return e
			}
			report, e := s.ReconcileJournal(ctx)
			if e != nil {
				return e
			}
			after, e := s.GetMessage(ctx, sc.Tenant, sc.Message)
			if e != nil || after.Status != "delivered" {
				return fmt.Errorf("journal failed to restore delivered: %+v %v", after, e)
			}
			if e = s.Control.Release(ctx, report.Digest); e != nil {
				return e
			}
			if _, e = s.Claim(ctx, domain.Job{TenantID: sc.Tenant, MessageID: sc.Message}, "restore-drill"); e != domain.ErrNotFound {
				return fmt.Errorf("restored message was claimable: %v", e)
			}
			c, _ := client.New(api, sc.Key)
			replay, e := c.Send(ctx, "physical-restore-key", sc.Request)
			if e != nil || replay.MessageIDs[0] != sc.Message {
				return fmt.Errorf("idempotency changed after restore: %v", e)
			}
			resp, e := http.Get("http://sink:8025/counts")
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
			if counts.ByMessage[sc.Message] != 1 {
				return fmt.Errorf("expected one SMTP copy, got %d", counts.ByMessage[sc.Message])
			}
			fmt.Println("PHYSICAL PITR PASSED: queued snapshot, independent journal restored delivered, no second claim, SMTP copies=1")
			return nil
		} else {
			return fmt.Errorf("unknown mode")
		}
	}
	b, _ := json.Marshal(sc)
	if e = os.WriteFile(file, b, 0600); e != nil {
		return e
	}
	fmt.Printf("Recovery drill %s: message=%s target=%s\n", mode, sc.Message, sc.Target)
	return nil
}
