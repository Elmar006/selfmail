package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/telemetry"
)

func cleanupCommand(ctx context.Context, s *store.Store, args []string) error {
	f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	apply := f.Bool("apply", false, "apply bounded cleanup instead of dry run")
	if e := f.Parse(args); e != nil {
		return e
	}
	result, e := s.Cleanup(ctx, store.DefaultRetention(), !*apply)
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func runMaintenance(ctx context.Context, s *store.Store) error {
	next := time.Now()
	for ctx.Err() == nil {
		op, cancel := context.WithTimeout(ctx, 15*time.Second)
		n, e := s.AggregateStatistics(op)
		telemetry.Progress("statistics", e)
		cancel()
		if e != nil {
			slog.Error("statistics aggregation failed", "error", e)
		}
		if time.Now().After(next) {
			op, cancel := context.WithTimeout(ctx, 30*time.Second)
			result, err := s.Cleanup(op, store.DefaultRetention(), false)
			cancel()
			telemetry.Progress("retention", err)
			if err != nil {
				slog.Warn("retention deferred", "error", err)
			}
			next = time.Now().Add(retentionDelay(result, err))
		}
		if n == 1000 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("maintenance stopped: %w", ctx.Err())
}

func retentionDelay(result store.CleanupResult, e error) time.Duration {
	if e != nil {
		return 30 * time.Second
	}
	if result.More {
		return 2 * time.Second
	}
	return time.Hour
}
