package main

import (
	"errors"
	"github.com/Elmar006/selfmail/internal/store"
	"testing"
	"time"
)

func TestMaintenanceContinuesBoundedCleanup(t *testing.T) {
	if retentionDelay(store.CleanupResult{More: true}, nil) > 2*time.Second {
		t.Fatal("retention backlog delayed for an hour")
	}
	if retentionDelay(store.CleanupResult{}, errors.New("transient")) >= time.Hour {
		t.Fatal("failed cleanup waits too long")
	}
	if retentionDelay(store.CleanupResult{}, nil) != time.Hour {
		t.Fatal("idle cleanup should back off")
	}
}
