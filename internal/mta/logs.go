package mta

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/store"
	"github.com/Elmar006/selfmail/internal/telemetry"
)

type LogEvent struct{ QueueID, MessageID, AttemptID, Status, Recipient, DSN, Diagnostic string }

var lineRE = regexp.MustCompile(`postfix/(?:cleanup|qmgr|smtp|lmtp|error|bounce)\[[0-9]+\]: ([A-Za-z0-9]+): (.*)`)
var messageRE = regexp.MustCompile(`message-id=<([a-f0-9-]{36})\.([a-f0-9-]{36})@[^>]+>`)
var deliveryRE = regexp.MustCompile(`to=<([^>]+)>.*\bdsn=([245]\.[0-9]+\.[0-9]+), status=(sent|deferred|bounced)\b(?: (.*))?`)

func ParseLog(line string) (LogEvent, bool) {
	var ev LogEvent
	m := lineRE.FindStringSubmatch(line)
	if len(m) != 3 {
		return ev, false
	}
	ev.QueueID = m[1]
	if id := messageRE.FindStringSubmatch(m[2]); len(id) == 3 && domain.ValidID(id[1]) && domain.ValidID(id[2]) {
		ev.MessageID = id[1]
		ev.AttemptID = id[2]
		return ev, true
	}
	if strings.HasPrefix(m[2], "from=<") && strings.Contains(m[2], "(queue active)") {
		ev.Status = "active"
		return ev, true
	}
	if d := deliveryRE.FindStringSubmatch(m[2]); len(d) == 5 {
		ev.Recipient, _ = domain.Address(d[1])
		ev.DSN = d[2]
		ev.Status = d[3]
		ev.Diagnostic = d[4]
		if len(ev.Diagnostic) > 512 {
			ev.Diagnostic = ev.Diagnostic[:512]
		}
		return ev, true
	}
	return ev, false
}
func FileID(f *os.File) (string, error) {
	info, e := f.Stat()
	if e != nil {
		return "", e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return "", e
	}
	first, e := bufio.NewReaderSize(f, 64*1024).ReadSlice('\n')
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(first)
	identity := ""
	// Portable reflection keeps the repository buildable on Windows; Linux inode
	// and device distinguish rotations. The first complete line handles truncation.
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if v.IsValid() && v.Kind() == reflect.Struct {
		for _, key := range []string{"Dev", "Ino"} {
			field := v.FieldByName(key)
			if field.IsValid() && field.CanUint() {
				identity += fmt.Sprint(field.Uint()) + ":"
			}
		}
	}
	return identity + hex.EncodeToString(sum[:]), nil
}
func ScanFile(ctx context.Context, s *store.Store, node, path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	id, e := FileID(f)
	if errors.Is(e, io.EOF) {
		return nil
	}
	if e != nil {
		return e
	}
	offset, e := s.LogCursor(ctx, node, id)
	if e != nil {
		return e
	}
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if info.Size() < offset {
		return fmt.Errorf("Postfix log truncated below persisted cursor: %s", path)
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	_, e = scanLogStream(ctx, s, node, id, offset, f)
	return e
}
func scanLogStream(ctx context.Context, s *store.Store, node, id string, offset int64, source io.Reader) (int64, error) {
	r := bufio.NewReaderSize(source, 64*1024)
	for ctx.Err() == nil {
		line, e := r.ReadSlice('\n')
		if errors.Is(e, io.EOF) {
			return offset, nil
		}
		if e != nil {
			return offset, e
		}
		if len(line) > 64*1024 {
			return offset, fmt.Errorf("oversized Postfix log line")
		}
		next := offset + int64(len(line))
		ev, ok := ParseLog(string(line))
		if e = s.ApplyLogLine(ctx, node, id, offset, next, ev.QueueID, ev.MessageID, ev.AttemptID, ev.Status, ev.Recipient, ev.DSN, ev.Diagnostic, ok); e != nil {
			return offset, e
		}
		offset = next
	}
	return offset, ctx.Err()
}
func RunLogs(ctx context.Context, s *store.Store, node, path string) error {
	completed := map[string]string{}
	for ctx.Err() == nil {
		telemetry.ReadSpool(path)
		files, e := filepath.Glob(path + "*")
		telemetry.Progress("reconciler_scan", e)
		if e != nil {
			return e
		}
		sort.Slice(files, func(i, j int) bool {
			a, ea := os.Stat(files[i])
			b, eb := os.Stat(files[j])
			return ea == nil && eb == nil && a.ModTime().Before(b.ModTime())
		})
		for _, p := range files {
			info, statErr := os.Stat(p)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			stamp := fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
			if p != path && completed[p] == stamp {
				continue
			}
			var done bool
			if strings.HasSuffix(p, ".gz") {
				done, e = scanCompressed(ctx, s, node, p)
			} else {
				e = ScanFile(ctx, s, node, p)
				if e == nil && p != path {
					done, e = markIngested(ctx, s, node, p)
				}
			}
			if e != nil && !errors.Is(e, context.Canceled) {
				slog.Error("Postfix reconciliation failed", "path", p, "error", e)
			}
			telemetry.Progress("reconciler", e)
			if e == nil && done {
				completed[p] = stamp
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
