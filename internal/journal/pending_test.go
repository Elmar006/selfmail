package journal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/security"
)

func TestInterruptedJournalWriteRepair(t *testing.T) {
	for _, phase := range []string{"before-record", "before-manifest", "after-manifest"} {
		t.Run(phase, func(t *testing.T) {
			v, _ := security.NewVault(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			j, _ := New(t.TempDir(), v)
			h, e := j.checkedHead()
			if e != nil {
				t.Fatal(e)
			}
			r := Record{ID: ID("pending"), Kind: "intent", At: time.Now().UTC(), Sequence: 1}
			if e = j.startPending(r.ID, 1); e != nil {
				t.Fatal(e)
			}
			if phase != "before-record" {
				b, _ := json.Marshal(r)
				b, _ = v.Seal(b, "selfmail:journal:v1:"+r.ID)
				p, _ := j.path(r.ID)
				os.MkdirAll(filepath.Dir(p), 0700)
				if e = os.WriteFile(p, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if phase == "after-manifest" {
				if e = j.appendEntry(h, r.ID); e != nil {
					t.Fatal(e)
				}
			}
			if e = j.CheckHead(context.Background()); e == nil {
				t.Fatal("pending journal admitted work")
			}
			if e = j.RepairPending(context.Background()); e != nil {
				t.Fatal(e)
			}
			if _, e = j.Digest(context.Background()); e != nil {
				t.Fatal(e)
			}
			n := 0
			j.Visit(context.Background(), func(Record) error { n++; return nil })
			want := 1
			if phase == "before-record" {
				want = 0
			}
			if n != want {
				t.Fatalf("records %d want %d", n, want)
			}
		})
	}
}
