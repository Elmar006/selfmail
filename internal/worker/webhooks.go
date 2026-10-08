package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/telemetry"
	"github.com/jackc/pgx/v5"
)

func WebhookOne(ctx context.Context, s *store.Store, vault *security.Vault, client *http.Client) (bool, error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	var id, endpoint, event, url string
	var payload, encrypted []byte
	var attempt int
	// Endpoint before job is also the repair/replay lock order. Different
	// endpoints progress independently; one poison endpoint cannot deadlock all.
	e = tx.QueryRow(ctx, `SELECT w.id::text,w.url,w.encrypted_secret FROM webhook_endpoints w WHERE w.enabled AND EXISTS(SELECT 1 FROM webhook_jobs j WHERE j.endpoint_id=w.id AND j.status='pending' AND j.available_at<=clock_timestamp()) ORDER BY w.id FOR UPDATE OF w SKIP LOCKED LIMIT 1`).Scan(&endpoint, &url, &encrypted)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	secret, e := vault.Open(encrypted, "webhook:"+endpoint)
	if e == nil {
		e = tx.QueryRow(ctx, "SELECT id::text,event_id::text,payload,attempt_count FROM webhook_jobs WHERE endpoint_id=$1 AND status='pending' AND available_at<=clock_timestamp() ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1", endpoint).Scan(&id, &event, &payload, &attempt)
		if errors.Is(e, pgx.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
	}
	if e != nil {
		_, e = tx.Exec(ctx, "UPDATE webhook_jobs SET status='dead',attempt_count=attempt_count+1,last_error='permanent: encrypted endpoint secret unavailable',available_at=clock_timestamp() WHERE endpoint_id=$1 AND status='pending'", endpoint)
		if e != nil {
			return false, e
		}
		if _, e = tx.Exec(ctx, "UPDATE webhook_endpoints SET enabled=false WHERE id=$1", endpoint); e != nil {
			return false, e
		}
		return true, tx.Commit(ctx)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	request, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if e != nil {
		_, e = tx.Exec(ctx, "UPDATE webhook_jobs SET status='dead',attempt_count=attempt_count+1,last_error='permanent: invalid endpoint URL',available_at=clock_timestamp() WHERE id=$1", id)
		if e != nil {
			return false, e
		}
		return true, tx.Commit(ctx)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Selfmail-Event-ID", event)
	request.Header.Set("X-Selfmail-Timestamp", ts)
	request.Header.Set("X-Selfmail-Signature", "v1="+security.Sign(secret, ts, payload))
	response, e := client.Do(request)
	success := false
	reason := ""
	if e == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		success = response.StatusCode >= 200 && response.StatusCode < 300
		reason = fmt.Sprintf("HTTP %d", response.StatusCode)
	} else {
		reason = truncate(e.Error(), 256)
	}
	status := "pending"
	if success {
		status = "delivered"
	} else if attempt+1 >= 12 {
		status = "dead"
	}
	delay := store.RetryDelay(attempt + 1)
	delay += time.Duration(rand.Int64N(max(int64(delay/4), 1)))
	_, e = tx.Exec(ctx, "UPDATE webhook_jobs SET status=$2,attempt_count=attempt_count+1,available_at=clock_timestamp()+$3::interval,last_error=$4 WHERE id=$1", id, status, store.Interval(delay), reason)
	if e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
func RunWebhooks(ctx context.Context, s *store.Store, vault *security.Vault, allowPrivate bool) error {
	client := security.WebhookClient(allowPrivate)
	defer client.CloseIdleConnections()
	for ctx.Err() == nil {
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		did, e := WebhookOne(op, s, vault, client)
		telemetry.Progress("webhooks", e)
		cancel()
		if e != nil && ctx.Err() == nil {
			slog.Warn("webhook delivery will retry", "error", e)
		}
		if (!did || e != nil) && !pause(ctx, time.Second) {
			break
		}
	}
	return ctx.Err()
}
