package journal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Elmar006/selfmail/internal/security"
)

func batchJournal(t *testing.T) *Journal {
	t.Helper()
	v, e := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	j, e := New(t.TempDir(), v)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Bind(context.Background(), t.TempDir(), "batch-test", true); e != nil {
		t.Fatal(e)
	}
	return j
}

func testBatchRecords(prefix string) []Record {
	var records []Record
	for i := range 3 {
		records = append(records, Record{ID: ID(prefix, fmt.Sprint(i)), Kind: "log", Node: "test", File: prefix, Offset: int64(i * 100), Next: int64((i + 1) * 100)})
	}
	return records
}

func TestJournalBatchIdempotencyAndCollision(t *testing.T) {
	j := batchJournal(t)
	ctx := context.Background()
	records := testBatchRecords("batch")
	if e := j.PutMany(records); e != nil {
		t.Fatal(e)
	}
	before, e := j.Digest(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.PutMany(records); e != nil {
		t.Fatal(e)
	}
	after, e := j.Digest(ctx)
	if e != nil || before != after {
		t.Fatal("batch replay changed committed journal", e)
	}
	// A fresh record before a collision must not be partially committed.
	fresh := testBatchRecords("fresh")[0]
	changed := records[1]
	changed.Diagnostic = "different"
	if e = j.PutMany([]Record{fresh, changed}); e == nil {
		t.Fatal("identity collision accepted")
	}
	if _, e = j.Get(fresh.ID); !os.IsNotExist(e) {
		t.Fatal("batch collision persisted an earlier record", e)
	}
	if e = j.PutMany([]Record{records[0], fresh, records[2]}); e != nil {
		t.Fatal(e)
	}
	if e = j.Put(fresh); e != nil {
		t.Fatal("single writer cannot replay a batch record", e)
	}
	if e = j.Visit(ctx, nil); e != nil {
		t.Fatal(e)
	}
}

func TestInterruptedJournalBatchRepair(t *testing.T) {
	for _, phase := range []string{"descriptor", "first-record", "records", "partial-manifest", "manifest", "head", "unsealed-missing-record"} {
		t.Run(phase, func(t *testing.T) {
			j := batchJournal(t)
			ctx := context.Background()
			if e := j.Put(Record{ID: ID("prior"), Kind: "intent"}); e != nil {
				t.Fatal(e)
			}
			h, e := j.checkedHead()
			if e != nil {
				t.Fatal(e)
			}
			records := testBatchRecords(phase)
			batch, e := j.prepareBatch(h, records)
			if e != nil {
				t.Fatal(e)
			}
			plain, _ := json.Marshal(batch)
			if e = j.writePending(plain); e != nil {
				t.Fatal(e)
			}
			if phase == "first-record" {
				path, _ := j.path(batch.Items[0].ID)
				if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					t.Fatal(e)
				}
				if e = writeBatchRecord(filepath.Dir(path), path, batch.Items[0].Cipher); e != nil {
					t.Fatal(e)
				}
			} else if phase != "descriptor" {
				if e = j.persistBatchRecords(batch.Items, false); e != nil {
					t.Fatal(e)
				}
			}
			if phase == "partial-manifest" || phase == "manifest" || phase == "unsealed-missing-record" {
				suffix, _ := j.batchSuffix(batch)
				if phase == "partial-manifest" {
					suffix = suffix[:len(batch.Items[0].Line)+5]
				}
				f, e := os.OpenFile(j.manifestPath(), os.O_WRONLY|os.O_APPEND, 0600)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.Write(suffix); e == nil {
					e = f.Sync()
				}
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
			} else if phase == "head" {
				if e = j.completeBatchManifest(batch); e != nil {
					t.Fatal(e)
				}
			}
			if phase == "unsealed-missing-record" {
				path, _ := j.path(batch.Items[1].ID)
				if e = os.Remove(path); e != nil {
					t.Fatal(e)
				}
			}
			if e = j.CheckHead(ctx); e == nil {
				t.Fatal("interrupted batch admitted work")
			}
			if e = j.RepairPending(ctx); e != nil {
				t.Fatal(e)
			}
			if e = j.PutMany(records); e != nil {
				t.Fatal("repaired batch cannot be replayed", e)
			}
			n := 0
			if e = j.Visit(ctx, func(Record) error { n++; return nil }); e != nil || n != 4 {
				t.Fatal("repair lost or duplicated records", n, e)
			}
		})
	}
}

func TestJournalBatchRepairRejectsLostCommittedHistory(t *testing.T) {
	for _, fault := range []string{"sealed-record", "sealed-tail", "base-tail", "unexpected-suffix", "wrong-head", "wrong-target"} {
		t.Run(fault, func(t *testing.T) {
			j := batchJournal(t)
			if e := j.Put(Record{ID: ID("prior"), Kind: "intent"}); e != nil {
				t.Fatal(e)
			}
			h, _ := j.checkedHead()
			batch, e := j.prepareBatch(h, testBatchRecords(fault))
			if e != nil {
				t.Fatal(e)
			}
			if fault == "wrong-target" {
				batch.Target.Sequence++
			}
			plain, _ := json.Marshal(batch)
			if e = j.writePending(plain); e != nil {
				t.Fatal(e)
			}
			if e = j.persistBatchRecords(batch.Items, false); e != nil {
				t.Fatal(e)
			}
			switch fault {
			case "sealed-record", "sealed-tail":
				if e = j.completeBatchManifest(batch); e != nil {
					t.Fatal(e)
				}
				if fault == "sealed-record" {
					path, _ := j.path(batch.Items[0].ID)
					e = os.Remove(path)
				} else {
					e = os.Truncate(j.manifestPath(), batch.Target.Offset-1)
				}
			case "base-tail":
				e = os.Truncate(j.manifestPath(), h.Offset-1)
			case "unexpected-suffix":
				f, err := os.OpenFile(j.manifestPath(), os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, e = f.Write([]byte("invalid\n"))
				f.Close()
			case "wrong-head":
				h.Bytes++
				e = j.writeHead(h)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = j.RepairPending(context.Background()); e == nil {
				t.Fatal("repair concealed committed loss or descriptor mismatch")
			}
			if _, e = os.Stat(filepath.Join(j.root, "pending.enc")); e != nil {
				t.Fatal("failed repair removed the fence", e)
			}
		})
	}
}

func TestMixedConcurrentSingleAndBatchWriters(t *testing.T) {
	j := batchJournal(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other, e := New(j.root, j.vault)
			if e == nil {
				e = other.Bind(context.Background(), j.anchor, j.instance, false)
			}
			if e == nil {
				records := testBatchRecords(fmt.Sprint(i))
				if i%2 == 0 {
					e = other.Put(records[0])
				} else {
					e = other.PutMany(records)
				}
			}
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	n := 0
	if e := j.Visit(context.Background(), func(Record) error { n++; return nil }); e != nil || n != 32 {
		t.Fatal("mixed writers lost records", n, e)
	}
}
