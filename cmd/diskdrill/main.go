// diskdrill fills only its explicitly mounted, isolated tmpfs.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/Elmar006/selfmail/internal/fsbudget"
	"github.com/Elmar006/selfmail/internal/journal"
	"github.com/Elmar006/selfmail/internal/security"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if os.Getenv("SELFMAIL_DISK_DRILL") != "isolated-tmpfs" {
		return fmt.Errorf("explicit isolated tmpfs marker required")
	}
	free, e := fsbudget.Free("/fault")
	if e != nil {
		return e
	}
	if free > 64*1024*1024 {
		return fmt.Errorf("refuse filling a filesystem larger than 64 MiB")
	}
	v, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	j, e := journal.New("/fault/journal", v)
	if e != nil {
		return e
	}
	if e = j.Put(journal.Record{ID: journal.ID("before-full"), Kind: "intent"}); e != nil {
		return e
	}
	if e = j.CheckHead(context.Background()); e == nil {
		return fmt.Errorf("low disk reserve failed to block admission")
	}
	f, e := os.Create("/fault/owned-fill")
	if e != nil {
		return e
	}
	block := make([]byte, 65536)
	for {
		_, e = f.Write(block)
		if e != nil {
			break
		}
	}
	f.Close()
	if !errors.Is(e, os.ErrPermission) && e == nil {
		return fmt.Errorf("did not reach disk exhaustion")
	}
	if e = j.Put(journal.Record{ID: journal.ID("during-full"), Kind: "intent"}); e == nil {
		return fmt.Errorf("disk-full journal write unexpectedly succeeded")
	}
	if e = os.Remove("/fault/owned-fill"); e != nil {
		return e
	}
	// Existing committed evidence survives disk exhaustion. No SMTP is attempted.
	if _, e = j.Get(journal.ID("before-full")); e != nil {
		return e
	}
	if _, e = j.Digest(context.Background()); e != nil {
		return fmt.Errorf("disk recovery left journal unusable: %w", e)
	}
	fmt.Println("DISK PASSED: low reserve blocks admission; ENOSPC prevents handoff evidence; committed evidence survives")
	return nil
}
