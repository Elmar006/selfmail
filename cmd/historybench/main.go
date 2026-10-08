// historybench measures exact production queries in a newly created database.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/telemetry"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if os.Getenv("APP_ENV") != "development" {
		return fmt.Errorf("development-only large history benchmark")
	}
	u, e := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if e != nil || u.Host != "postgres:5432" || u.User.Username() != "postgres" {
		return fmt.Errorf("isolated test admin database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	admin, e := store.Open(ctx, u.String())
	if e != nil {
		return e
	}
	defer admin.Close()
	name := "selfmail_perf_" + strings.ReplaceAll(domain.ID(), "-", "")
	if _, e = admin.Pool.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		return e
	}
	defer admin.Pool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	u.Path = "/" + name
	s, e := store.Open(ctx, u.String())
	if e != nil {
		return e
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		return e
	}
	tenant, _, e := s.CreateTenant(ctx, "million-row-history", 50, 100000)
	if e != nil {
		return e
	}
	conn, e := s.Pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, "SET statement_timeout='5min'"); e != nil {
		return e
	}
	seedStart := time.Now()
	for _, query := range []string{
		`INSERT INTO batches(id,tenant_id,idempotency_key,idempotency_digest,fingerprint) SELECT md5('batch'||i)::uuid,$1,'bench-'||i,encode(sha256(convert_to('bench-'||i,'UTF8')),'hex'),'fixture' FROM generate_series(0,9999) i`,
		`INSERT INTO messages(id,tenant_id,batch_id,sender,recipient,priority,status,next_attempt_at,expires_at,created_at,updated_at) SELECT md5('message'||i)::uuid,$1,md5('batch'||((i-1)/100))::uuid,'n@example.test','r@example.net','normal','delivered',clock_timestamp()-interval '60 days',clock_timestamp()-interval '59 days',clock_timestamp()-interval '60 days',clock_timestamp()-interval '59 days' FROM generate_series(1,1000000) i`,
		`INSERT INTO outbox(tenant_id,message_id,priority,available_at,published_at) SELECT $1,id,'normal',created_at,created_at FROM messages WHERE tenant_id=$1`,
	} {
		if _, e = conn.Exec(ctx, query, tenant); e != nil {
			return e
		}
	}
	// Completed-history stock is backfilled once. Runtime scrapes touch 32 shards.
	if _, e = conn.Exec(ctx, `INSERT INTO statistics_stock(status,shard,count) SELECT status,mod(abs(hashtext(id::text)::bigint),32)::int,count(*) FROM messages GROUP BY status,mod(abs(hashtext(id::text)::bigint),32)::int;DELETE FROM statistics_deltas;ANALYZE`); e != nil {
		return e
	}
	for i := 0; i < 100; i++ {
		id := domain.ID()
		status := "queued"
		next := time.Now().Add(time.Hour)
		if i < 10 {
			next = time.Now().Add(-time.Minute)
		}
		if _, e = conn.Exec(ctx, `INSERT INTO messages(id,tenant_id,batch_id,sender,recipient,priority,status,next_attempt_at,expires_at) VALUES($1,$2,md5('batch0')::uuid,'n@example.test','r@example.net','normal',$3,$4,clock_timestamp()+interval '1 day')`, id, tenant, status, next); e != nil {
			return e
		}
	}
	fmt.Printf("HISTORY SEEDED final_rows=1000000 active_rows=100 seed=%s\n", time.Since(seedStart).Round(time.Millisecond))
	queries := []string{
		`SELECT m.id FROM messages m WHERE status='dispatching' AND lease_until<clock_timestamp() ORDER BY lease_until,id FOR UPDATE SKIP LOCKED LIMIT 100`,
		`SELECT m.id FROM messages m WHERE status='queued' AND next_attempt_at<=now() AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.message_id=m.id AND o.published_at IS NULL) AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.message_id=m.id AND o.published_at>now()-interval '5 minutes') ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 100`,
		`SELECT status,sum(count) FROM statistics_stock GROUP BY status`,
		`SELECT coalesce(extract(epoch FROM clock_timestamp()-min(next_attempt_at)),0) FROM messages WHERE status='queued' AND next_attempt_at<=clock_timestamp()`,
	}
	for _, query := range queries {
		rows, e := conn.Query(ctx, "EXPLAIN (ANALYZE,BUFFERS) "+query)
		if e != nil {
			return e
		}
		for rows.Next() {
			var line string
			if e = rows.Scan(&line); e != nil {
				rows.Close()
				return e
			}
			fmt.Println(line)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	before := time.Now()
	if e = s.Recover(ctx); e != nil {
		return e
	}
	watchdog := time.Since(before)
	h := telemetry.Handler(s, true)
	req := httptest.NewRequest("GET", "/metrics", nil)
	recorder := httptest.NewRecorder()
	before = time.Now()
	h.ServeHTTP(recorder, req)
	scrape := time.Since(before)
	io.Copy(io.Discard, recorder.Result().Body)
	if watchdog > time.Second || scrape > time.Second || recorder.Code != 200 {
		return fmt.Errorf("capacity target missed watchdog=%s scrape=%s status=%d", watchdog, scrape, recorder.Code)
	}
	fmt.Printf("HISTORY PASSED final_rows=1000000 watchdog_ms=%d scrape_ms=%d\n", watchdog.Milliseconds(), scrape.Milliseconds())
	return nil
}
