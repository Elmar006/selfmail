package mta

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
)

func TestLogBatchFlushesCompleteLinesAndWithholdsPartialTail(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("integration DB required")
	}
	ctx := context.Background()
	s, e := store.Open(ctx, os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	file := domain.ID()
	complete := strings.Repeat("unparsed log line\n", store.MaxLogBatch+1)
	offset, e := scanLogStream(ctx, s, "batch-tail", file, 0, strings.NewReader(complete+"incomplete"))
	if e != nil || offset != int64(len(complete)) {
		t.Fatal("partial tail advanced cursor or lost complete lines", offset, e)
	}
	if got, e := s.LogCursor(ctx, "batch-tail", file); e != nil || got != offset {
		t.Fatal("SQL cursor differs from committed chunk", got, e)
	}
	got, e := scanLogStream(ctx, s, "batch-tail", file, offset, strings.NewReader("incomplete\n"))
	if e != nil || got != offset+int64(len("incomplete\n")) {
		t.Fatal("tail was not resumed", got, e)
	}
	file = domain.ID()
	got, e = scanLogStream(ctx, s, "batch-tail", file, 0, strings.NewReader("complete\n"+strings.Repeat("x", 65537)))
	if e == nil || got != int64(len("complete\n")) {
		t.Fatal("oversized tail lost committed prefix", got, e)
	}
}
