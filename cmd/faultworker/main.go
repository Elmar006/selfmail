// faultworker is a development-only OOM drill after real sink acceptance.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/mailmsg"
	"github.com/Elmar006/selfmail/internal/mta"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/worker"
)

type scenario struct{ Database, Tenant, Message string }
type noLimit struct{}

func (noLimit) Take(context.Context, string, string, int) (time.Duration, error) { return 0, nil }

type oomTransport struct{}

func (oomTransport) LocalHostname() string { return "fault.example.test" }
func (oomTransport) Submit(ctx context.Context, from, to, attempt string, raw []byte) mta.Result {
	c := mta.Client{Address: "sink:1025", Hostname: "fault.example.test", Timeout: 10 * time.Second}
	result := c.Submit(ctx, from, to, attempt, raw)
	if result.Err != nil {
		return result
	}
	os.WriteFile("/fault/accepted.marker", []byte("confirmed sink acceptance before OOM"), 0600)
	var blocks [][]byte
	for {
		block := make([]byte, 4*1024*1024)
		for i := 0; i < len(block); i += 4096 {
			block[i] = 1
		}
		blocks = append(blocks, block)
		runtime.KeepAlive(blocks)
	}
}
func main() {
	mode := flag.String("mode", "prepare", "prepare|crash|verify")
	flag.Parse()
	if e := run(*mode); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(mode string) error {
	if os.Getenv("APP_ENV") != "development" {
		return fmt.Errorf("development-only isolated fault")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	u, e := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if e != nil || u.Host != "postgres:5432" || u.User.Username() != "postgres" {
		return fmt.Errorf("isolated test admin required")
	}
	var sc scenario
	if mode == "prepare" {
		admin, e := store.Open(ctx, u.String())
		if e != nil {
			return e
		}
		defer admin.Close()
		sc.Database = "selfmail_oom_" + strings.ReplaceAll(domain.ID(), "-", "")
		if _, e = admin.Pool.Exec(ctx, "CREATE DATABASE "+sc.Database); e != nil {
			return e
		}
	} else {
		b, e := os.ReadFile("/fault/case.json")
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &sc); e != nil {
			return e
		}
		if !strings.HasPrefix(sc.Database, "selfmail_oom_") {
			return fmt.Errorf("invalid owned fault database")
		}
	}
	u.Path = "/" + sc.Database
	s, e := store.Open(ctx, u.String())
	if e != nil {
		return e
	}
	defer s.Close()
	v, e := security.NewVault(os.Getenv("MASTER_KEY"))
	if e != nil {
		return e
	}
	s.Control, e = recovery.New("/fault/control", v)
	if e != nil {
		return e
	}
	s.Journal, e = journal.New("/fault/journal", v)
	if e != nil {
		return e
	}
	if mode == "prepare" {
		if e = s.Migrate(ctx); e != nil {
			return e
		}
		if e = s.Control.Initialize(ctx); e != nil {
			return e
		}
		id, key, e := s.CreateTenant(ctx, "owned-oom-drill", 50, 1000)
		if e != nil {
			return e
		}
		sc.Tenant = id
		p, e := s.Authenticate(ctx, key)
		if e != nil {
			return e
		}
		priv, pub, e := mailmsg.GenerateKey()
		if e != nil {
			return e
		}
		did := domain.ID()
		encrypted, e := v.Seal(priv, "dkim:"+did)
		if e != nil {
			return e
		}
		if e = s.AddDomain(ctx, id, domain.Domain{ID: did, Name: "oom.example.test", Selector: "selfmail", Token: domain.Token(), PublicKey: pub, EncryptedKey: encrypted}); e != nil {
			return e
		}
		req := domain.SendRequest{From: "notify@oom.example.test", To: []string{"oom+" + domain.ID() + "@example.net"}, Subject: "OOM after confirmed DATA", Text: "one copy", Priority: "critical", TTLSeconds: 86400}
		r, e := s.Enqueue(ctx, p, "oom-idempotency", "oom-fingerprint", req, true)
		if e != nil {
			return e
		}
		sc.Message = r.MessageIDs[0]
		b, _ := json.Marshal(sc)
		if e = os.WriteFile("/fault/case.json", b, 0600); e != nil {
			return e
		}
		fmt.Println("OOM scenario prepared")
		return nil
	}
	if mode == "crash" {
		w := worker.Worker{Store: s, Limiter: noLimit{}, Vault: v, MTA: oomTransport{}, NodeID: "oom-node", AllowUnverified: true}
		return w.Handle(ctx, domain.Job{TenantID: sc.Tenant, MessageID: sc.Message})
	}
	if mode != "verify" {
		return fmt.Errorf("unknown mode")
	}
	if _, e = os.Stat(filepath.Join("/fault", "accepted.marker")); e != nil {
		return fmt.Errorf("OOM did not reach DATA acceptance: %w", e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE messages SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", sc.Message); e != nil {
		return e
	}
	if e = s.Recover(ctx); e != nil {
		return e
	}
	m, e := s.GetMessage(ctx, sc.Tenant, sc.Message)
	if e != nil || m.Status != "submission_unknown" {
		return fmt.Errorf("OOM recovery expected unknown: %+v %v", m, e)
	}
	if _, e = s.Claim(ctx, domain.Job{TenantID: sc.Tenant, MessageID: sc.Message}, "second-worker"); e != domain.ErrNotFound {
		return fmt.Errorf("OOM message automatically claimable: %v", e)
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
		return fmt.Errorf("SMTP copies=%d", counts.ByMessage[sc.Message])
	}
	fmt.Println("OOM PASSED: confirmed SMTP copy=1; sending lease recovered to unknown; automatic resend denied")
	return nil
}
