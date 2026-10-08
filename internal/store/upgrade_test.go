package store

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestUpgradeFrom005PreservesDeliveryStates(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, e := Open(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := "selfmail_upgrade_" + strings.ReplaceAll(domain.ID(), "-", "")
	if _, e = admin.Pool.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	defer admin.Pool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	u, _ := url.Parse(raw)
	u.Path = "/" + name
	s, e := Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.Pool.Exec(ctx, "CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		t.Fatal(e)
	}
	files, _ := migrations.ReadDir("migrations")
	for _, f := range files {
		if f.Name()[:3] > "005" {
			continue
		}
		b, _ := migrations.ReadFile("migrations/" + f.Name())
		if _, e = s.Pool.Exec(ctx, string(b)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", f.Name()); e != nil {
			t.Fatal(e)
		}
	}
	tenant := domain.ID()
	if _, e = s.Pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES($1,'legacy-five')", tenant); e != nil {
		t.Fatal(e)
	}
	states := []string{"queued", "dispatching", "submission_unknown", "submitted", "deferred", "delivered", "bounced", "failed", "canceled", "suppressed"}
	ids := map[string]string{}
	for _, status := range states {
		batch, mid := domain.ID(), domain.ID()
		ids[status] = mid
		if _, e = s.Pool.Exec(ctx, "INSERT INTO batches(id,tenant_id,idempotency_key,fingerprint) VALUES($1,$2,$3,'legacy-hash')", batch, tenant, status); e != nil {
			t.Fatal(e)
		}
		payload, _ := json.Marshal(domain.SendRequest{From: "a@example.test", To: []string{"b@example.net"}, Subject: "legacy", Text: "preserved body", Priority: "normal"})
		if _, e = s.Pool.Exec(ctx, "INSERT INTO batch_payloads(batch_id,tenant_id,payload) VALUES($1,$2,$3)", batch, tenant, payload); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, "INSERT INTO messages(id,tenant_id,batch_id,sender,recipient,priority,status,next_attempt_at,expires_at) VALUES($1,$2,$3,'a@example.test','b@example.net','normal',$4,clock_timestamp(),clock_timestamp()+interval '1 day')", mid, tenant, batch, status); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, "INSERT INTO events(id,tenant_id,message_id,type) VALUES($1,$2,$3,$4)", domain.ID(), tenant, mid, status); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.VerifySchema(ctx); e == nil {
		t.Fatal("old schema accepted before migration")
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	if e = s.VerifySchema(ctx); e != nil {
		t.Fatal(e)
	}
	for status, id := range ids {
		m, e := s.GetMessage(ctx, tenant, id)
		if e != nil || m.Status != status {
			t.Fatalf("legacy %s changed: %+v %v", status, m, e)
		}
		ev, e := s.Events(ctx, tenant, id)
		if e != nil || len(ev) != 1 || ev[0].Sequence != 1 {
			t.Fatal("event backfill", e)
		}
		replay, exists, e := s.Replay(ctx, tenant, status, "legacy-hash")
		if e != nil || !exists || replay.MessageIDs[0] != id {
			t.Fatal("legacy idempotency lost", e)
		}
	}
}
