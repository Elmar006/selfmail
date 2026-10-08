package journal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Elmar006/selfmail/internal/recovery"
	"github.com/Elmar006/selfmail/internal/security"
)

// CopySnapshot captures the manifest boundary under a short lock, then copies
// immutable evidence without blocking live writers. The destination must be new.
func (j *Journal) CopySnapshot(ctx context.Context, root, anchor string) error {
	if root == j.root || anchor == j.anchor {
		return fmt.Errorf("snapshot cannot replace active storage")
	}
	lock, e := recovery.LockFile(ctx, filepath.Join(j.root, "manifest.lock"), true)
	if e != nil {
		return e
	}
	h, e := j.checkedHead()
	if e != nil {
		lock.Close()
		return e
	}
	headCipher, e := os.ReadFile(j.headPath())
	if e != nil {
		lock.Close()
		return e
	}
	manifest, e := os.Open(j.manifestPath())
	lock.Close()
	if e != nil {
		return e
	}
	defer manifest.Close()
	if e = os.Mkdir(root, 0700); e != nil {
		return e
	}
	if e = os.Mkdir(anchor, 0700); e != nil {
		return e
	}
	// Only the sealed prefix is copied, even when new records are appended.
	source := io.NewSectionReader(manifest, 0, h.Offset)
	if e = writeDurable(filepath.Join(root, "manifest.log"), source); e != nil {
		return e
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(manifest, 0, h.Offset), 2048)
	var sequence uint64
	previous := ""
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		line, readErr := reader.ReadSlice('\n')
		if readErr == io.EOF && len(line) == 0 {
			break
		}
		if readErr != nil {
			return readErr
		}
		item, e := j.decodeEntry(line)
		if e != nil {
			return e
		}
		sequence++
		if item.Sequence != sequence || item.Previous != previous {
			return fmt.Errorf("snapshot manifest chain")
		}
		previous = security.Digest(string(line))
		from, e := j.path(item.ID)
		if e != nil {
			return e
		}
		cipher, e := os.ReadFile(from)
		if e != nil {
			return e
		}
		if len(cipher) > 64*1024 || security.Digest(string(cipher)) != item.Checksum {
			return fmt.Errorf("snapshot evidence integrity")
		}
		dir := filepath.Join(root, item.ID[:2])
		if e = os.MkdirAll(dir, 0700); e != nil {
			return e
		}
		if e = writeDurable(filepath.Join(dir, item.ID+".enc"), strings.NewReader(string(cipher))); e != nil {
			return e
		}
	}
	if sequence != h.Sequence || previous != h.Hash {
		return fmt.Errorf("snapshot incomplete manifest")
	}
	if e = writeDurable(filepath.Join(anchor, "journal-head.enc"), strings.NewReader(string(headCipher))); e != nil {
		return e
	}
	return syncDirectory(root)
}

func writeDurable(to string, source io.Reader) error {
	dest, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(dest, source)
	if e == nil {
		e = dest.Sync()
	}
	closeErr := dest.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(filepath.Dir(to))
}
