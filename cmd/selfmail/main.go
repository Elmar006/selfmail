package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Elmar006/selfmail/internal/application"
	"github.com/Elmar006/selfmail/internal/config"
	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/httpapi"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/limiter"
	"github.com/Elmar006/selfmail/internal/mta"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/sink"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/submission"
	"github.com/Elmar006/selfmail/internal/telemetry"
	"github.com/Elmar006/selfmail/internal/worker"
)

var version = "dev"
var revision = "unknown"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx); e != nil && !errors.Is(e, context.Canceled) {
		slog.Error("service stopped", "error", e)
		os.Exit(1)
	}
}
func run(ctx context.Context) error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: selfmail api|worker|dispatcher|reconciler|webhooks|bounce|migrate|tenant|key|resolve|sink|health")
	}
	mode := os.Args[1]
	if mode == "version" {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "revision": revision, "go": runtime.Version()})
	}
	if mode == "sink" {
		return sink.Run(ctx)
	}
	if mode == "health" {
		client := http.Client{Timeout: 2 * time.Second}
		r, e := client.Get("http://127.0.0.1:8080/healthz")
		if e != nil {
			return e
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("health returned %d", r.StatusCode)
		}
		return nil
	}
	cfg, e := config.Load()
	if e != nil {
		return e
	}
	s, e := store.Open(ctx, cfg.DatabaseURL)
	if e != nil {
		return e
	}
	defer s.Close()
	s.Observe = telemetry.ObserveWork
	if mode == "migrate" {
		return s.Migrate(ctx)
	}
	if e = s.VerifySchema(ctx); e != nil {
		return e
	}
	vault, e := security.NewVault(cfg.MasterKey)
	if e != nil {
		return e
	}
	if cfg.ControlDir != "" {
		s.Control, e = recovery.New(cfg.ControlDir, vault)
		if e != nil {
			return e
		}
	}
	if cfg.JournalDir != "" {
		s.Journal, e = journal.New(cfg.JournalDir, vault)
		if e != nil {
			return e
		}
		s.Journal.MaxBytes = cfg.JournalMaxBytes
		s.Journal.Observe = telemetry.ObserveWork
	}
	if mode == "initialize" {
		if s.Control == nil || s.Journal == nil {
			return fmt.Errorf("initialize requires control and journal directories")
		}
		if e = s.VerifyVault(ctx, vault, true); e != nil {
			return e
		}
		state, e := s.Control.Status(ctx)
		if e != nil {
			return e
		}
		if state.Instance == "" {
			var n int
			if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM messages").Scan(&n); e != nil {
				return e
			}
			empty, e := s.Journal.Empty(ctx)
			if e != nil {
				return e
			}
			if n != 0 || !empty {
				return fmt.Errorf("existing delivery history requires recovery hold and reconciliation")
			}
		}
		if e = s.Control.Initialize(ctx); e != nil {
			return e
		}
		state, e = s.Control.Status(ctx)
		if e != nil {
			return e
		}
		return s.Journal.Bind(ctx, cfg.ControlDir, state.Instance, true)
	}
	if mode == "recovery" && len(os.Args) > 2 && os.Args[2] == "import-control" {
		if e = s.VerifyVault(ctx, vault, false); e != nil {
			return e
		}
		return importControlCommand(ctx, s, cfg.ControlDir, os.Args[3:])
	}
	if mode == "recovery" && len(os.Args) > 2 && (os.Args[2] == "status" || os.Args[2] == "hold") {
		return recoveryCommand(ctx, s, os.Args[2:])
	}
	legacySeed := mode == "recovery" && len(os.Args) > 2 && os.Args[2] == "seed-upgrade"
	if s.Control != nil && s.Journal != nil {
		state, e := s.Control.Status(ctx)
		if e != nil {
			return e
		}
		if state.Instance != "" {
			if e = s.Journal.Bind(ctx, cfg.ControlDir, state.Instance, legacySeed && state.Held); e != nil {
				if mode == "recovery" && len(os.Args) > 2 && os.Args[2] == "repair-journal" && state.Held {
					return recoveryCommand(ctx, s, os.Args[2:])
				}
				return e
			}
		}
		if e = s.CheckJournal(ctx); e != nil {
			return e
		}
	}
	if s.Control != nil || s.Journal != nil {
		if e = s.VerifyVault(ctx, vault, legacySeed); e != nil {
			return e
		}
	}
	service := &application.Service{Repo: s, Vault: vault, AllowUnverified: cfg.AllowUnverified, WireLimit: cfg.WireLimit, Budget: application.NewWorkBudget(2)}
	if mode == "recovery" {
		return recoveryCommand(ctx, s, os.Args[2:])
	}
	if mode == "cleanup" {
		return cleanupCommand(ctx, s, os.Args[2:])
	}
	if mode == "webhook" {
		return webhookCommand(ctx, s, vault, os.Args[2:])
	}
	if mode == "evidence" {
		return evidenceCommand(ctx, s, os.Args[2:])
	}
	if mode == "tenant" || mode == "key" || mode == "resolve" {
		return admin(ctx, s, service, mode, os.Args[2:])
	}
	metrics := &http.Server{Addr: ":9090", Handler: telemetry.Handler(s, mode == "worker"), ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if e := metrics.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			slog.Error("metrics server failed", "error", e)
		}
	}()
	defer metrics.Close()
	bounceDomain := os.Getenv("BOUNCE_DOMAIN")
	bounceKey, _ := base64.StdEncoding.DecodeString(cfg.MasterKey)
	switch mode {
	case "maintainer":
		return runMaintenance(ctx, s)
	case "api":
		lim, e := limiter.Open(cfg.RedisURL)
		if e != nil {
			return e
		}
		defer lim.Client.Close()
		service.Limiter = lim
		api := &httpapi.API{Service: service, AllowPrivateWebhooks: cfg.AllowPrivateWebhooks, Ready: func(ctx context.Context) error {
			if e := s.CheckAdmission(ctx); e != nil {
				return e
			}
			return lim.Client.Ping(ctx).Err()
		}}
		httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
		smtpServer, e := submission.New(service, cfg.SMTPAddr, cfg.SMTPHostname, cfg.CertFile, cfg.KeyFile, cfg.AllowPlainSMTP)
		if e != nil {
			return e
		}
		errs := make(chan error, 2)
		go func() { errs <- httpServer.ListenAndServe() }()
		go func() { errs <- smtpServer.ListenAndServe() }()
		slog.Info("submission service ready", "http", cfg.HTTPAddr, "smtp", cfg.SMTPAddr)
		select {
		case <-ctx.Done():
		case e = <-errs:
		}
		stop, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		httpServer.Shutdown(stop)
		smtpServer.Shutdown(stop)
		smtpServer.Close()
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case "worker":
		lim, e := limiter.Open(cfg.RedisURL)
		if e != nil {
			return e
		}
		defer lim.Client.Close()
		lim.DestinationRate = cfg.DestinationRate
		w := &worker.Worker{Store: s, Limiter: lim, Vault: vault, MTA: &mta.Client{Address: cfg.PostfixAddr, Hostname: cfg.SMTPHostname, Timeout: cfg.SMTPTimeout}, NodeID: cfg.NodeID, AllowUnverified: cfg.AllowUnverified, BounceDomain: bounceDomain, BounceKey: bounceKey, WireLimit: cfg.WireLimit}
		return worker.RunConsumers(ctx, w, cfg.RabbitURL, cfg.Concurrency)
	case "dispatcher":
		return worker.RunDispatcher(ctx, s, cfg.RabbitURL)
	case "reconciler":
		return mta.RunLogs(ctx, s, cfg.NodeID, cfg.PostfixLog)
	case "webhooks":
		return worker.RunWebhooks(ctx, s, vault, cfg.AllowPrivateWebhooks)
	case "bounce":
		if !domain.ValidDomain(bounceDomain) {
			return fmt.Errorf("BOUNCE_DOMAIN required")
		}
		srv := mta.NewBounceServer(s, ":2526", bounceDomain, bounceKey)
		errch := make(chan error, 1)
		go func() { errch <- srv.ListenAndServe() }()
		select {
		case e := <-errch:
			return e
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return srv.Shutdown(stop)
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
}
func admin(ctx context.Context, s *store.Store, svc *application.Service, mode string, args []string) error {
	if mode == "tenant" {
		if len(args) < 1 {
			return fmt.Errorf("usage: tenant create|pause|resume")
		}
		f := flag.NewFlagSet("tenant", flag.ContinueOnError)
		name := f.String("name", "", "project name")
		sender := f.String("domain", "", "optional sender domain")
		id := f.String("id", "", "tenant UUID")
		rate := f.Int("rate", 10, "messages per second")
		daily := f.Int("daily-limit", 10000, "accepted recipients per UTC day")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if args[0] == "pause" || args[0] == "resume" {
			if !domain.ValidID(*id) {
				return domain.Invalid("id", "UUID required")
			}
			return s.PauseTenant(ctx, *id, args[0] == "pause")
		}
		if args[0] != "create" {
			return fmt.Errorf("unknown tenant operation")
		}
		tenant, key, e := s.CreateTenant(ctx, *name, *rate, *daily)
		if e != nil {
			return e
		}
		out := map[string]any{"tenant_id": tenant, "api_key": key}
		if *sender != "" {
			d, e := svc.CreateDomain(ctx, tenant, *sender)
			if e != nil {
				return fmt.Errorf("tenant %s created; domain creation failed: %w", tenant, e)
			}
			out["domain"] = d
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if mode == "key" {
		if len(args) < 1 {
			return fmt.Errorf("usage: key issue|revoke")
		}
		f := flag.NewFlagSet("key", flag.ContinueOnError)
		tenant := f.String("tenant", "", "tenant UUID")
		token := f.String("token", "", "API key to revoke")
		scopes := f.String("scopes", "messages:write,messages:read", "comma-separated scopes")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if args[0] == "revoke" {
			return s.RevokeKey(ctx, *token)
		}
		if args[0] != "issue" || !domain.ValidID(*tenant) {
			return fmt.Errorf("key issue requires tenant UUID")
		}
		key, e := s.IssueKey(ctx, *tenant, strings.Split(*scopes, ","))
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"api_key": key})
	}
	f := flag.NewFlagSet("resolve", flag.ContinueOnError)
	id := f.String("id", "", "message UUID")
	reason := f.String("reason", "", "operator evidence")
	confirm := f.Bool("confirm-not-in-queue", false, "operator verified Postfix queue and archived logs; duplicate risk accepted")
	if e := f.Parse(args); e != nil {
		return e
	}
	if !domain.ValidID(*id) || len(*reason) < 10 || !*confirm {
		return fmt.Errorf("resolve requires --id, --reason (10+ characters), --confirm-not-in-queue")
	}
	return s.ResolveUnknown(ctx, *id, *reason)
}
