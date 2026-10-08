package main

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
)

func evidenceCommand(ctx context.Context, s *store.Store, args []string) error {
	if len(args) < 1 || args[0] != "export" || s.Journal == nil || s.Control == nil {
		return fmt.Errorf("evidence export requires independent journal/control")
	}
	f := flag.NewFlagSet("evidence export", flag.ContinueOnError)
	directory := f.String("directory", "/evidence", "export staging parent")
	envfile := f.String("env-file", "", "private deployment environment file to include")
	secretDir := f.String("secrets-directory", "/secrets", "private PostgreSQL backup passphrase directory")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if *envfile == "" {
		return fmt.Errorf("--env-file required for recoverable keys/configuration")
	}
	state, e := s.Control.Status(ctx)
	if e != nil {
		return e
	}
	root := filepath.Join(*directory, time.Now().UTC().Format("20060102T150405Z")+"-"+domain.ID())
	if e = os.Mkdir(root, 0700); e != nil {
		return e
	}
	if e = s.Journal.CopySnapshot(ctx, filepath.Join(root, "journal"), filepath.Join(root, "anchor")); e != nil {
		return e
	}
	final, e := s.Control.Status(ctx)
	if e != nil {
		return e
	}
	if final.Instance != state.Instance || final.Generation != state.Generation {
		return fmt.Errorf("recovery generation changed during export")
	}
	// Environment is temporarily private on the staging filesystem. Restic then
	// encrypts it with a separate repository password, not MASTER_KEY.
	input, e := os.Open(*envfile)
	if e != nil {
		return e
	}
	defer input.Close()
	b, e := io.ReadAll(io.LimitReader(input, 65537))
	if e != nil {
		return e
	}
	if len(b) > 65536 {
		return fmt.Errorf("environment export limit")
	}
	if e = validateEvidenceEnvironment(b, os.Getenv("MASTER_KEY"), os.Getenv("APP_ENV")); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(root, "deployment.env"), b, 0600); e != nil {
		return e
	}
	pass, e := os.ReadFile(filepath.Join(*secretDir, "backup_passphrase"))
	if e != nil {
		return e
	}
	if len(pass) < 32 || len(pass) > 256 {
		return fmt.Errorf("backup passphrase export required")
	}
	if e = os.WriteFile(filepath.Join(root, "postgres-backup-passphrase"), pass, 0600); e != nil {
		return e
	}
	b, e = json.Marshal(state)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(root, "instance.json"), b, 0600); e != nil {
		return e
	}
	fmt.Printf("Independent evidence export: %s\n", root)
	return nil
}

func validateEvidenceEnvironment(raw []byte, activeKey, activeEnv string) error {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if !ok || (key != "MASTER_KEY" && key != "APP_ENV") {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return fmt.Errorf("duplicate recovery environment setting")
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"'")
	}
	exported, e := base64.StdEncoding.DecodeString(values["MASTER_KEY"])
	active, activeErr := base64.StdEncoding.DecodeString(activeKey)
	if e != nil || activeErr != nil || len(exported) != 32 || !hmac.Equal(exported, active) {
		return fmt.Errorf("exported MASTER_KEY does not match the active deployment")
	}
	if values["APP_ENV"] != activeEnv {
		return fmt.Errorf("exported APP_ENV does not match the active deployment")
	}
	return nil
}
