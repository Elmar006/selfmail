package journal

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Elmar006/selfmail/internal/fsbudget"
	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

// The head is stored in the independent control volume in deployments. It
// detects truncation of the append-only manifest, including loss of its tail.
type head struct {
	Instance  string `json:"instance"`
	Sequence  uint64 `json:"sequence"`
	Offset    int64  `json:"offset"`
	LastStart int64  `json:"last_start"`
	Hash      string `json:"hash"`
	Bytes     uint64 `json:"bytes"`
}
type entry struct {
	Sequence uint64 `json:"sequence"`
	ID       string `json:"id"`
	Checksum string `json:"checksum"`
	Previous string `json:"previous"`
}

func (j *Journal) manifestPath() string { return filepath.Join(j.root, "manifest.log") }
func (j *Journal) headPath() string     { return filepath.Join(j.anchor, "journal-head.enc") }

// Bind associates evidence with one deployment. Initialization is allowed only
// for an empty journal; loss of an existing manifest/head is never auto-healed.
func (j *Journal) Bind(ctx context.Context, anchor, instance string, initialize bool) error {
	if anchor == "" || instance == "" {
		return fmt.Errorf("journal instance binding required")
	}
	j.anchor, j.instance, j.bound = anchor, instance, true
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), true)
	if e != nil {
		return e
	}
	defer p.Close()
	_, e = j.readHead()
	if errors.Is(e, os.ErrNotExist) && initialize {
		e = j.initializeHead()
	}
	if e != nil {
		return e
	}
	_, e = j.checkedHead()
	if e == nil {
		h, err := j.readHead()
		if err != nil {
			return err
		}
		if h.Sequence > 0 && h.Bytes == 0 {
			h.Bytes = uint64(h.Offset)
			err = filepath.WalkDir(j.root, func(path string, item os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if e = ctx.Err(); e != nil {
					return e
				}
				if item.IsDir() || len(item.Name()) != 68 || !strings.HasSuffix(item.Name(), ".enc") {
					return nil
				}
				stat, e := item.Info()
				if e != nil {
					return e
				}
				h.Bytes += uint64(stat.Size())
				return nil
			})
			if err != nil {
				return err
			}
			e = j.writeHead(h)
		}
	}
	return e
}
func (j *Journal) readHead() (h head, e error) {
	b, e := os.ReadFile(j.headPath())
	if e != nil {
		return h, e
	}
	b, e = j.vault.Open(b, "selfmail:journal-head:v1:"+j.instance)
	if e != nil {
		return h, fmt.Errorf("journal head integrity/key/instance: %w", e)
	}
	if e = json.Unmarshal(b, &h); e != nil {
		return h, e
	}
	if h.Instance != j.instance || h.Offset < 0 || h.LastStart < 0 || h.LastStart > h.Offset || (h.Sequence > 0 && len(h.Hash) != 64) {
		return h, fmt.Errorf("invalid journal head")
	}
	return h, nil
}
func (j *Journal) writeHead(h head) error {
	b, e := json.Marshal(h)
	if e != nil {
		return e
	}
	b, e = j.vault.Seal(b, "selfmail:journal-head:v1:"+j.instance)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(j.anchor, ".journal-head-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
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
	if e = os.Rename(f.Name(), j.headPath()); e != nil {
		return e
	}
	return syncDirectory(j.anchor)
}
func (j *Journal) initializeHead() error {
	items, e := os.ReadDir(j.root)
	if e != nil {
		return e
	}
	for _, item := range items {
		if item.Name() != "manifest.lock" {
			return fmt.Errorf("missing journal head with existing evidence; recovery required")
		}
	}
	f, e := os.OpenFile(j.manifestPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = f.Sync()
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = syncDirectory(j.root); e != nil {
		return e
	}
	return j.writeHead(head{Instance: j.instance})
}
func (j *Journal) checkedHead() (h head, e error) {
	if _, e = os.Stat(filepath.Join(j.root, "pending.enc")); e == nil {
		return h, fmt.Errorf("interrupted journal write; hold and repair-journal required")
	} else if !errors.Is(e, os.ErrNotExist) {
		return h, e
	}
	return j.checkedHeadWithoutPending()
}
func (j *Journal) checkedHeadWithoutPending() (h head, e error) {
	h, e = j.readHead()
	if errors.Is(e, os.ErrNotExist) && !j.bound {
		e = j.initializeHead()
		if e == nil {
			h, e = j.readHead()
		}
	}
	if e != nil {
		return h, e
	}
	f, e := os.Open(j.manifestPath())
	if e != nil {
		return h, e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return h, e
	}
	if stat.Size() != h.Offset {
		return h, fmt.Errorf("journal manifest/head mismatch: incomplete write or lost history; recovery hold required")
	}
	if h.Sequence == 0 {
		if h.Offset != 0 {
			return h, fmt.Errorf("invalid empty journal")
		}
		return h, nil
	}
	if h.Offset-h.LastStart > 2048 {
		return h, fmt.Errorf("journal manifest entry limit")
	}
	line := make([]byte, h.Offset-h.LastStart)
	if _, e = f.ReadAt(line, h.LastStart); e != nil {
		return h, e
	}
	if security.Digest(string(line)) != h.Hash {
		return h, fmt.Errorf("journal manifest tail integrity")
	}
	last, e := j.decodeEntry(line)
	if e != nil {
		return h, e
	}
	if last.Sequence != h.Sequence {
		return h, fmt.Errorf("journal sequence mismatch")
	}
	return h, nil
}

func (j *Journal) CheckHead(ctx context.Context) error {
	free, e := fsbudget.Free(j.root)
	if e != nil {
		return e
	}
	if free < 128*1024*1024 {
		return fmt.Errorf("journal disk reserve exhausted")
	}
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), false)
	if e != nil {
		return e
	}
	defer p.Close()
	h, e := j.checkedHead()
	if e == nil && h.Bytes >= j.capacity() {
		return fmt.Errorf("journal capacity exhausted")
	}
	return e
}

func (j *Journal) startPending(id string, sequence uint64) error {
	b, e := json.Marshal(entry{ID: id, Sequence: sequence})
	if e != nil {
		return e
	}
	b, e = j.vault.Seal(b, "selfmail:journal-pending:v1:"+j.instance)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(j.root, "pending.enc"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(filepath.Join(j.root, "pending.enc"))
		}
	}()
	_, e = f.Write(b)
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
	if e = syncDirectory(j.root); e != nil {
		return e
	}
	committed = true
	return nil
}
func (j *Journal) finishPending() error {
	if e := os.Remove(filepath.Join(j.root, "pending.enc")); e != nil {
		return e
	}
	return syncDirectory(j.root)
}

// RepairPending completes only the single authenticated interrupted operation.
// Missing committed history is never repaired by dropping evidence.
func (j *Journal) RepairPending(ctx context.Context) error {
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), true)
	if e != nil {
		return e
	}
	defer p.Close()
	b, e := os.ReadFile(filepath.Join(j.root, "pending.enc"))
	if errors.Is(e, os.ErrNotExist) {
		_, e = j.checkedHead()
		return e
	}
	if e != nil {
		return e
	}
	b, e = j.vault.Open(b, "selfmail:journal-pending:v1:"+j.instance)
	if e != nil {
		return e
	}
	var pending entry
	if e = json.Unmarshal(b, &pending); e != nil {
		return e
	}
	h, e := j.readHead()
	if e != nil {
		return e
	}
	path, e := j.path(pending.ID)
	if e != nil {
		return e
	}
	f, e := os.Open(j.manifestPath())
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return e
	}
	if stat.Size() < h.Offset || stat.Size()-h.Offset > 2048 {
		return fmt.Errorf("cannot repair missing or excessive manifest history")
	}
	r, e := j.Get(pending.ID)
	if errors.Is(e, os.ErrNotExist) && stat.Size() == h.Offset && pending.Sequence == h.Sequence+1 {
		return j.finishPending()
	}
	if e != nil {
		return e
	}
	if r.Sequence != pending.Sequence {
		return fmt.Errorf("pending evidence sequence mismatch")
	}
	if h.Sequence == pending.Sequence {
		if _, e = j.checkedHeadWithoutPending(); e != nil {
			return e
		}
		return j.finishPending()
	}
	if pending.Sequence != h.Sequence+1 {
		return fmt.Errorf("pending manifest sequence mismatch")
	}
	if stat.Size() == h.Offset {
		if e = j.appendEntry(h, pending.ID); e != nil {
			return e
		}
		return j.finishPending()
	}
	line := make([]byte, stat.Size()-h.Offset)
	if _, e = f.ReadAt(line, h.Offset); e != nil {
		return e
	}
	item, e := j.decodeEntry(line)
	if e != nil {
		return e
	}
	cipher, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if item.Sequence != pending.Sequence || item.ID != pending.ID || item.Previous != h.Hash || item.Checksum != security.Digest(string(cipher)) {
		return fmt.Errorf("unverified manifest tail")
	}
	if e = j.writeHead(head{Instance: j.instance, Sequence: item.Sequence, Offset: stat.Size(), LastStart: h.Offset, Hash: security.Digest(string(line)), Bytes: h.Bytes + uint64(len(cipher)+len(line))}); e != nil {
		return e
	}
	return j.finishPending()
}
func (j *Journal) decodeEntry(line []byte) (r entry, e error) {
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return r, fmt.Errorf("truncated journal manifest")
	}
	b, e := base64.StdEncoding.DecodeString(strings.TrimSuffix(string(line), "\n"))
	if e != nil {
		return r, e
	}
	b, e = j.vault.Open(b, "selfmail:journal-manifest:v1:"+j.instance)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	return r, e
}
func (j *Journal) appendEntry(h head, id string) error {
	path, e := j.path(id)
	if e != nil {
		return e
	}
	cipher, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	r := entry{Sequence: h.Sequence + 1, ID: id, Checksum: security.Digest(string(cipher)), Previous: h.Hash}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	b, e = j.vault.Seal(b, "selfmail:journal-manifest:v1:"+j.instance)
	if e != nil {
		return e
	}
	line := []byte(base64.StdEncoding.EncodeToString(b) + "\n")
	f, e := os.OpenFile(j.manifestPath(), os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(line)
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
	return j.writeHead(head{Instance: j.instance, Sequence: r.Sequence, LastStart: h.Offset, Offset: h.Offset + int64(len(line)), Hash: security.Digest(string(line)), Bytes: h.Bytes + uint64(len(cipher)+len(line))})
}

// Visit validates the complete chain and every referenced encrypted record with
// bounded memory. The immutable prefix is a snapshot; callers compare Digest
// after their work to detect concurrent appends.
func (j *Journal) Visit(ctx context.Context, visit func(Record) error) error {
	p, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), true)
	if e != nil {
		return e
	}
	h, e := j.checkedHead()
	p.Close()
	if e != nil {
		return e
	}
	f, e := os.Open(j.manifestPath())
	if e != nil {
		return e
	}
	defer f.Close()
	reader := bufio.NewReaderSize(io.LimitReader(f, h.Offset), 2048)
	previous := ""
	var sequence uint64
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		line, readErr := reader.ReadSlice('\n')
		if readErr == io.EOF && len(line) == 0 {
			break
		}
		if readErr != nil {
			return fmt.Errorf("invalid journal manifest: %w", readErr)
		}
		r, e := j.decodeEntry(line)
		if e != nil {
			return e
		}
		sequence++
		if r.Sequence != sequence || r.Previous != previous {
			return fmt.Errorf("journal manifest chain mismatch")
		}
		previous = security.Digest(string(line))
		path, e := j.path(r.ID)
		if e != nil {
			return e
		}
		cipher, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("missing journal evidence: %w", e)
		}
		if len(cipher) > 64*1024 || security.Digest(string(cipher)) != r.Checksum {
			return fmt.Errorf("journal record checksum mismatch")
		}
		record, e := j.Get(r.ID)
		if e != nil {
			return e
		}
		if visit != nil {
			if e = visit(record); e != nil {
				return e
			}
		}
	}
	if sequence != h.Sequence || previous != h.Hash {
		return fmt.Errorf("journal incomplete manifest")
	}
	return nil
}
