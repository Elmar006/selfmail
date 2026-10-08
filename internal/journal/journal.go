// Package journal preserves encrypted delivery evidence independently of a SQL
// snapshot. Records are immutable and durable before a handoff can begin.
package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

type Record struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Sequence    uint64    `json:"journal_sequence"`
	Tenant      string
	Message     string
	Attempt     string
	Batch       string
	KeyDigest   string
	Fingerprint string
	From        string
	Recipient   string
	Priority    string
	Node        string
	Queue       string
	Status      string
	File        string
	DSN         string
	Diagnostic  string
	Action      string
	Created     time.Time
	Offset      int64
	Next        int64
	MessageIDs  []string
	Recipients  []string
}
type Journal struct {
	root     string
	vault    *security.Vault
	anchor   string
	instance string
	bound    bool
	MaxBytes uint64
	// Observe is configured before use; it reports bounded operation timings.
	Observe func(string, time.Duration)
}

func (j *Journal) capacity() uint64 {
	if j.MaxBytes == 0 {
		return 10 * 1024 * 1024 * 1024
	}
	return j.MaxBytes
}

func New(root string, vault *security.Vault) (*Journal, error) {
	if root == "" || vault == nil {
		return nil, fmt.Errorf("journal directory and vault required")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	return &Journal{root: root, vault: vault, anchor: root, instance: "local"}, nil
}
func ID(parts ...string) string { return security.Digest(strings.Join(parts, "\x00")) }
func (j *Journal) path(id string) (string, error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("invalid record id")
	}
	return filepath.Join(j.root, id[:2], id+".enc"), nil
}
func (j *Journal) Get(id string) (r Record, e error) {
	path, e := j.path(id)
	if e != nil {
		return r, e
	}
	f, e := os.Open(path)
	if e != nil {
		return r, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if e != nil {
		return r, e
	}
	if len(b) > 64*1024 {
		return r, fmt.Errorf("oversized journal record")
	}
	b, e = j.vault.Open(b, "selfmail:journal:v1:"+id)
	if e != nil {
		return r, fmt.Errorf("journal integrity/key error: %w", e)
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.ID != id || r.At.IsZero() {
		return r, fmt.Errorf("invalid journal identity")
	}
	return
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (j *Journal) Put(r Record) error {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), true)
	if j.Observe != nil {
		j.Observe("journal_lock_wait", time.Since(started))
	}
	if e != nil {
		return e
	}
	locked := time.Now()
	defer func() {
		p.Close()
		if j.Observe != nil {
			j.Observe("journal_write", time.Since(locked))
		}
	}()
	h, e := j.checkedHead()
	if e != nil {
		return e
	}
	path, e := j.path(r.ID)
	if e != nil {
		return e
	}
	if old, e := j.Get(r.ID); e == nil {
		if old.Sequence == 0 || old.Sequence > h.Sequence {
			return fmt.Errorf("journal record is not committed in manifest; recovery required")
		}
		r.At = old.At
		r.Sequence = old.Sequence
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(r)
		if string(a) != string(b) {
			return fmt.Errorf("journal identity collision")
		}
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	r.At = time.Now().UTC()
	r.Sequence = h.Sequence + 1
	plain, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if len(plain) > 60*1024 {
		return fmt.Errorf("journal record limit")
	}
	cipher, e := j.vault.Seal(plain, "selfmail:journal:v1:"+r.ID)
	if e != nil {
		return e
	}
	if h.Bytes+uint64(len(cipher)+2048) > j.capacity() {
		return fmt.Errorf("journal capacity exhausted")
	}
	if e = j.startPending(r.ID, r.Sequence); e != nil {
		return e
	}
	dir := filepath.Dir(path)
	// Only a newly created shard changes the root directory. Existing shards
	// were synced before their first committed record.
	if e = os.Mkdir(dir, 0700); e == nil {
		e = syncDirectory(j.root)
	} else if errors.Is(e, os.ErrExist) {
		var info os.FileInfo
		info, e = os.Stat(dir)
		if e == nil && !info.IsDir() {
			e = fmt.Errorf("journal shard is not a directory")
		}
	}
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".record-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(cipher)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Link(f.Name(), path); e != nil {
		return e
	}
	if e = os.Remove(f.Name()); e != nil {
		return e
	}
	if e = syncDirectory(dir); e != nil {
		return e
	}
	if e = j.appendCipherEntry(h, r.ID, cipher); e != nil {
		return e
	}
	return j.finishPending()
}
func (j *Journal) Records(ctx context.Context) ([]Record, error) {
	var records []Record
	e := j.Visit(ctx, func(r Record) error {
		if len(records) >= 100000 {
			return fmt.Errorf("use streaming journal Visit for large history")
		}
		records = append(records, r)
		return nil
	})
	if e != nil {
		return nil, e
	}
	return records, nil
}
func (j *Journal) Empty(ctx context.Context) (bool, error) {
	items, e := os.ReadDir(j.root)
	if e != nil {
		return false, e
	}
	if len(items) == 0 {
		return true, nil
	}
	var n int
	e = j.Visit(ctx, func(Record) error { n++; return nil })
	return n == 0, e
}
func (j *Journal) Digest(ctx context.Context) (string, error) {
	if e := j.Visit(ctx, nil); e != nil {
		return "", e
	}
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), false)
	if e != nil {
		return "", e
	}
	defer p.Close()
	h, e := j.checkedHead()
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(h)
	if e != nil {
		return "", e
	}
	return security.Digest(string(b)), nil
}
