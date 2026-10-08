package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/store"
)

func recoveryCommand(parent context.Context, s *store.Store, args []string) error {
	if len(args) == 0 || s.Control == nil || s.Journal == nil {
		return fmt.Errorf("recovery status|hold|seed-upgrade|reconcile|release requires persistent storage")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	f := flag.NewFlagSet("recovery", flag.ContinueOnError)
	reason := f.String("reason", "", "operator reason")
	report := f.String("report", "", "reconciliation SHA-256")
	keepUnknown := f.Bool("keep-unknown", false, "leave unresolved messages unsubmitted")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	switch args[0] {
	case "status":
		state, e := s.Control.Status(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(state)
	case "hold":
		return s.Control.Hold(ctx, *reason)
	case "seed-upgrade":
		if len(*reason) < 10 {
			return fmt.Errorf("reviewed upgrade reason required")
		}
		return s.SeedLegacyAcceptance(ctx)
	case "reconcile":
		result, e := s.ReconcileJournal(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "repair-journal":
		state, e := s.Control.Status(ctx)
		if e != nil {
			return e
		}
		if !state.Held {
			return fmt.Errorf("recovery hold required")
		}
		return s.Journal.RepairPending(ctx)
	case "release":
		digest, e := s.Journal.Digest(ctx)
		if e != nil {
			return e
		}
		if digest != *report {
			return fmt.Errorf("journal changed since reconciliation")
		}
		var unknown int
		if e := s.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE status='submission_unknown'").Scan(&unknown); e != nil {
			return e
		}
		if unknown > 0 && !*keepUnknown {
			return fmt.Errorf("%d unresolved messages: require --keep-unknown; no automatic resend", unknown)
		}
		return s.Control.Release(ctx, *report)
	default:
		return fmt.Errorf("unknown recovery operation")
	}
}

func importControlCommand(ctx context.Context, s *store.Store, anchor string, args []string) error {
	if s.Control == nil || s.Journal == nil {
		return fmt.Errorf("persistent restored control/journal required")
	}
	f := flag.NewFlagSet("recovery import-control", flag.ContinueOnError)
	file := f.String("instance-file", "", "restored evidence instance.json")
	reason := f.String("reason", "", "operator recovery reason")
	if e := f.Parse(args); e != nil {
		return e
	}
	input, e := os.Open(*file)
	if e != nil {
		return e
	}
	defer input.Close()
	raw, e := io.ReadAll(io.LimitReader(input, 4097))
	if e != nil {
		return e
	}
	if len(raw) > 4096 {
		return fmt.Errorf("oversized recovery identity")
	}
	var source recovery.State
	if e = json.Unmarshal(raw, &source); e != nil {
		return e
	}
	// The encrypted head authenticates the original instance; full validation
	// rejects wrong keys, mixed exports, missing records and truncated manifests.
	if e = s.Journal.Bind(ctx, anchor, source.Instance, false); e != nil {
		return e
	}
	if e = s.CheckJournal(ctx); e != nil {
		return e
	}
	return s.Control.ImportHeld(ctx, source, *reason)
}
