package journal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

const MaxBatch = 32
const maxPendingBytes = 3 * 1024 * 1024

type batchItem struct {
	ID     string
	Cipher []byte
	Line   []byte
}

// A synced descriptor contains the exact uncommitted ciphertext and manifest
// suffix. Repair can complete this operation without inventing evidence or
// rewriting any prefix sealed by the independent head.
type pendingBatch struct {
	Version int
	Base    head
	Target  head
	Items   []batchItem
}

// PutMany groups durability barriers while keeping the v1 record, manifest and
// head formats. Successful return means every record and the final head have
// reached stable storage. No background/periodic flush or success-before-fsync.
func (j *Journal) PutMany(records []Record) error {
	if len(records) < 1 || len(records) > MaxBatch {
		return fmt.Errorf("journal batch requires 1..%d records", MaxBatch)
	}
	if len(records) == 1 {
		return j.Put(records[0])
	}
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
	batch, e := j.prepareBatch(h, records)
	if e != nil || len(batch.Items) == 0 {
		return e
	}
	plain, e := json.Marshal(batch)
	if e != nil {
		return e
	}
	if len(plain)+64 > maxPendingBytes {
		return fmt.Errorf("pending journal batch size limit")
	}
	if e = j.writePending(plain); e != nil {
		return e
	}
	if e = j.persistBatchRecords(batch.Items, false); e != nil {
		return e
	}
	if e = j.completeBatchManifest(batch); e != nil {
		return e
	}
	return j.finishPending()
}

func (j *Journal) prepareBatch(h head, records []Record) (pendingBatch, error) {
	batch := pendingBatch{Version: 2, Base: h, Target: h}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if _, e := j.path(record.ID); e != nil {
			return batch, e
		}
		if seen[record.ID] {
			return batch, fmt.Errorf("duplicate identity within journal batch")
		}
		seen[record.ID] = true
		old, e := j.Get(record.ID)
		if e == nil {
			if old.Sequence == 0 || old.Sequence > h.Sequence {
				return batch, fmt.Errorf("journal record is not committed in manifest")
			}
			record.At, record.Sequence = old.At, old.Sequence
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(record)
			if !bytes.Equal(a, b) {
				return batch, fmt.Errorf("journal identity collision")
			}
			continue
		}
		if !errors.Is(e, os.ErrNotExist) {
			return batch, e
		}
		record.At = time.Now().UTC()
		record.Sequence = batch.Target.Sequence + 1
		if record.Sequence == 0 {
			return batch, fmt.Errorf("journal sequence exhausted")
		}
		plain, e := json.Marshal(record)
		if e != nil {
			return batch, e
		}
		if len(plain) > 60*1024 {
			return batch, fmt.Errorf("journal record limit")
		}
		cipher, e := j.vault.Seal(plain, "selfmail:journal:v1:"+record.ID)
		if e != nil {
			return batch, e
		}
		item := entry{ID: record.ID, Sequence: record.Sequence, Checksum: security.Digest(string(cipher)), Previous: batch.Target.Hash}
		encoded, e := json.Marshal(item)
		if e != nil {
			return batch, e
		}
		encoded, e = j.vault.Seal(encoded, "selfmail:journal-manifest:v1:"+j.instance)
		if e != nil {
			return batch, e
		}
		line := []byte(base64.StdEncoding.EncodeToString(encoded) + "\n")
		batch.Target = nextBatchHead(batch.Target, item, cipher, line)
		if batch.Target.Bytes > j.capacity() {
			return batch, fmt.Errorf("journal capacity exhausted")
		}
		batch.Items = append(batch.Items, batchItem{ID: record.ID, Cipher: cipher, Line: line})
	}
	return batch, nil
}

func nextBatchHead(h head, item entry, cipher, line []byte) head {
	return head{Instance: h.Instance, Sequence: item.Sequence, LastStart: h.Offset, Offset: h.Offset + int64(len(line)), Hash: security.Digest(string(line)), Bytes: h.Bytes + uint64(len(cipher)+len(line))}
}

func (j *Journal) persistBatchRecords(items []batchItem, committed bool) error {
	dirs := make(map[string]bool, len(items))
	for _, item := range items {
		path, e := j.path(item.ID)
		if e != nil {
			return e
		}
		if old, e := readBatchCipher(path); e == nil {
			if !bytes.Equal(old, item.Cipher) {
				return fmt.Errorf("pending record ciphertext mismatch")
			}
			// A recovered file may have been linked before its directory sync.
			if !committed {
				f, e := os.Open(path)
				if e != nil {
					return e
				}
				e = f.Sync()
				f.Close()
				if e != nil {
					return e
				}
			}
			dirs[filepath.Dir(path)] = true
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if committed {
			return fmt.Errorf("committed journal record missing; cannot repair from pending copy")
		}
		dir := filepath.Dir(path)
		if e = os.Mkdir(dir, 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return e
		}
		if e = writeBatchRecord(dir, path, item.Cipher); e != nil {
			return e
		}
		dirs[dir] = true
	}
	if committed {
		return nil
	}
	var names []string
	for dir := range dirs {
		names = append(names, dir)
	}
	sort.Strings(names)
	for _, dir := range names {
		if e := syncDirectory(dir); e != nil {
			return e
		}
	}
	// This also syncs shard directories created before an interrupted write.
	// Repair cannot assume which mkdir operations reached storage earlier.
	return syncDirectory(j.root)
}

func readBatchCipher(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if e == nil && len(b) > 64*1024 {
		e = fmt.Errorf("journal ciphertext size limit")
	}
	return b, e
}

func writeBatchRecord(dir, path string, cipher []byte) error {
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
	return os.Remove(f.Name())
}

func (j *Journal) batchSuffix(batch pendingBatch) ([]byte, error) {
	if batch.Version != 2 || len(batch.Items) < 1 || len(batch.Items) > MaxBatch || batch.Base.Instance != j.instance {
		return nil, fmt.Errorf("invalid pending batch identity/size")
	}
	h := batch.Base
	var suffix []byte
	seen := make(map[string]bool, len(batch.Items))
	for _, item := range batch.Items {
		if _, e := j.path(item.ID); e != nil {
			return nil, e
		}
		if seen[item.ID] || len(item.Cipher) > 64*1024 || len(item.Line) > 2048 {
			return nil, fmt.Errorf("invalid pending batch item")
		}
		seen[item.ID] = true
		plain, e := j.vault.Open(item.Cipher, "selfmail:journal:v1:"+item.ID)
		if e != nil {
			return nil, e
		}
		var record Record
		if e = json.Unmarshal(plain, &record); e != nil {
			return nil, e
		}
		entry, e := j.decodeEntry(item.Line)
		if e != nil {
			return nil, e
		}
		if record.ID != item.ID || record.At.IsZero() || record.Sequence != h.Sequence+1 || entry.ID != item.ID || entry.Sequence != record.Sequence || entry.Previous != h.Hash || entry.Checksum != security.Digest(string(item.Cipher)) {
			return nil, fmt.Errorf("pending batch chain mismatch")
		}
		h = nextBatchHead(h, entry, item.Cipher, item.Line)
		suffix = append(suffix, item.Line...)
	}
	if h != batch.Target {
		return nil, fmt.Errorf("pending batch target mismatch")
	}
	return suffix, nil
}

func (j *Journal) completeBatchManifest(batch pendingBatch) error {
	suffix, e := j.batchSuffix(batch)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(j.manifestPath(), os.O_RDWR|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return e
	}
	if stat.Size() < batch.Base.Offset || stat.Size() > batch.Target.Offset {
		return fmt.Errorf("missing committed or unexpected manifest history")
	}
	// Verify the last sealed entry even when an unsealed suffix follows it.
	if batch.Base.Sequence > 0 {
		length := batch.Base.Offset - batch.Base.LastStart
		if length <= 0 || length > 2048 {
			return fmt.Errorf("invalid batch base head")
		}
		tail := make([]byte, length)
		if _, e = f.ReadAt(tail, batch.Base.LastStart); e != nil {
			return e
		}
		if security.Digest(string(tail)) != batch.Base.Hash {
			return fmt.Errorf("batch base manifest integrity")
		}
	} else if batch.Base.Offset != 0 {
		return fmt.Errorf("invalid empty batch base")
	}
	length := stat.Size() - batch.Base.Offset
	existing := make([]byte, length)
	if length > 0 {
		if _, e = f.ReadAt(existing, batch.Base.Offset); e != nil {
			return e
		}
	}
	if !bytes.Equal(existing, suffix[:length]) {
		return fmt.Errorf("unverified pending manifest suffix")
	}
	if _, e = f.Write(suffix[length:]); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	return j.writeHead(batch.Target)
}

func (j *Journal) repairBatch(batch pendingBatch) error {
	if _, e := j.batchSuffix(batch); e != nil {
		return e
	}
	h, e := j.readHead()
	if e != nil {
		return e
	}
	if h != batch.Base && h != batch.Target {
		return fmt.Errorf("pending batch does not match independent journal head")
	}
	committed := h == batch.Target
	if committed {
		if _, e = j.checkedHeadWithoutPending(); e != nil {
			return e
		}
	}
	if e = j.persistBatchRecords(batch.Items, committed); e != nil {
		return e
	}
	if e = j.completeBatchManifest(batch); e != nil {
		return e
	}
	return j.finishPending()
}

func (j *Journal) readPending() ([]byte, error) {
	f, e := os.Open(filepath.Join(j.root, "pending.enc"))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, maxPendingBytes+1))
	if e == nil && len(b) > maxPendingBytes {
		e = fmt.Errorf("pending journal size limit")
	}
	return b, e
}
