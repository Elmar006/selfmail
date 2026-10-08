package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
)

func webhookCommand(ctx context.Context, s *store.Store, vault *security.Vault, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("webhook repair|replay|resolve-dead")
	}
	f := flag.NewFlagSet("webhook", flag.ContinueOnError)
	tenant := f.String("tenant", "", "tenant UUID")
	id := f.String("endpoint", "", "endpoint UUID")
	reason := f.String("reason", "", "operator reason")
	secretFile := f.String("secret-file", "", "replacement receiver secret file")
	limit := f.Int("limit", 100, "bounded replay count")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if !domain.ValidID(*tenant) || !domain.ValidID(*id) {
		return fmt.Errorf("tenant and endpoint UUID required")
	}
	var n int64
	var e error
	if args[0] == "resolve-dead" {
		n, e = s.ResolveDeadWebhook(ctx, *tenant, *id, *reason)
	} else {
		var encrypted []byte
		if args[0] == "repair" {
			if *secretFile == "" {
				return fmt.Errorf("repair requires --secret-file; update receiver secret before replay")
			}
			b, err := os.ReadFile(*secretFile)
			if err != nil {
				return err
			}
			b = []byte(strings.TrimSpace(string(b)))
			if len(b) < 32 || len(b) > 256 {
				return fmt.Errorf("secret 32..256 bytes required")
			}
			encrypted, e = vault.Seal(b, "webhook:"+*id)
			if e != nil {
				return e
			}
		} else if args[0] != "replay" {
			return fmt.Errorf("unknown webhook operation")
		}
		n, e = s.RepairWebhook(ctx, *tenant, *id, *reason, encrypted, args[0] == "replay", *limit)
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"affected": n})
}
