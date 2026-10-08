package telemetry

import (
	"context"
	"net/http"
	"time"

	"github.com/Elmar006/selfmail/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type collector struct {
	s                                                                      *store.Store
	statuses, outbox, oldest, dead, logAge, up                             *prometheus.Desc
	statisticsAge, readyAge, deferredAge, hold, archivedAge, archiveErrors *prometheus.Desc
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.statuses, c.outbox, c.oldest, c.dead, c.logAge, c.up, c.statisticsAge, c.readyAge, c.deferredAge, c.hold, c.archivedAge, c.archiveErrors} {
		ch <- d
	}
}
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	up := 1.0
	rows, e := c.s.Pool.Query(ctx, "SELECT status,sum(count) FROM statistics_stock GROUP BY status")
	if e != nil {
		up = 0
	} else {
		for rows.Next() {
			var status string
			var n float64
			if e = rows.Scan(&status, &n); e != nil {
				up = 0
				break
			}
			ch <- prometheus.MustNewConstMetric(c.statuses, prometheus.GaugeValue, n, status)
		}
		if rows.Err() != nil {
			up = 0
		}
		rows.Close()
	}
	for _, q := range []struct {
		d   *prometheus.Desc
		sql string
	}{{c.outbox, "SELECT count(*) FROM outbox WHERE published_at IS NULL"}, {c.oldest, "SELECT coalesce(extract(epoch FROM clock_timestamp()-min(available_at)),0) FROM outbox WHERE published_at IS NULL AND available_at<=clock_timestamp()"}, {c.dead, "SELECT count(*) FROM webhook_jobs WHERE status='dead' AND resolved_at IS NULL"}, {c.logAge, "SELECT coalesce(extract(epoch FROM clock_timestamp()-max(updated_at)),0) FROM mta_log_cursors"},
		{c.statisticsAge, "SELECT coalesce(extract(epoch FROM clock_timestamp()-min(created_at)),0) FROM statistics_deltas"},
		{c.readyAge, "SELECT coalesce(extract(epoch FROM clock_timestamp()-min(next_attempt_at)),0) FROM messages WHERE status='queued' AND next_attempt_at<=now()"},
		{c.deferredAge, "SELECT coalesce(extract(epoch FROM clock_timestamp()-min(updated_at)),0) FROM messages WHERE status IN ('submitted','deferred')"},
		{c.archivedAge, "SELECT CASE WHEN current_setting('archive_mode')='off' THEN 0 ELSE coalesce(extract(epoch FROM clock_timestamp()-last_archived_time),-1) END FROM pg_stat_archiver"},
		{c.archiveErrors, "SELECT failed_count FROM pg_stat_archiver"}} {
		var n float64
		if e = c.s.Pool.QueryRow(ctx, q.sql).Scan(&n); e != nil {
			up = 0
			continue
		}
		ch <- prometheus.MustNewConstMetric(q.d, prometheus.GaugeValue, n)
	}
	held := 0.0
	if c.s.Control != nil {
		state, e := c.s.Control.Status(ctx)
		if e != nil || state.Held {
			held = 1
		}
	}
	ch <- prometheus.MustNewConstMetric(c.hold, prometheus.GaugeValue, held)
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up)
}
func Handler(s *store.Store, aggregate bool) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	reg.MustRegister(loops, successes, failures, consumers, outcomes, spool, spoolObserved)
	if aggregate {
		reg.MustRegister(&collector{s: s, statuses: prometheus.NewDesc("selfmail_messages", "Messages by delivery status", []string{"status"}, nil), outbox: prometheus.NewDesc("selfmail_outbox_pending", "Unpublished jobs", nil, nil), oldest: prometheus.NewDesc("selfmail_outbox_oldest_seconds", "Age of oldest ready unpublished job", nil, nil), dead: prometheus.NewDesc("selfmail_webhooks_dead", "Unresolved exhausted jobs", nil, nil), logAge: prometheus.NewDesc("selfmail_mta_log_age_seconds", "Age of log checkpoint", nil, nil), up: prometheus.NewDesc("selfmail_database_scrape_success", "DB metrics availability", nil, nil), statisticsAge: prometheus.NewDesc("selfmail_statistics_lag_seconds", "Age of oldest unapplied statistics delta", nil, nil), readyAge: prometheus.NewDesc("selfmail_queued_oldest_seconds", "Oldest ready message age", nil, nil), deferredAge: prometheus.NewDesc("selfmail_mta_pending_oldest_seconds", "Oldest submitted/deferred age", nil, nil), hold: prometheus.NewDesc("selfmail_recovery_hold", "Recovery fence active", nil, nil), archivedAge: prometheus.NewDesc("selfmail_wal_archive_age_seconds", "WAL archive age; -1 when never archived", nil, nil), archiveErrors: prometheus.NewDesc("selfmail_wal_archive_failures", "Cumulative archive errors", nil, nil)})
	}
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
