package mta

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Elmar006/selfmail/internal/security"
	"github.com/Elmar006/selfmail/internal/store"
)

const maxArchiveBytes = 100 * 1024 * 1024

type contextualReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextualReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func markIngested(ctx context.Context, s *store.Store, node, path string) (bool, error) {
	f, e := os.Open(path)
	if e != nil {
		return false, e
	}
	defer f.Close()
	id, e := FileID(f)
	if errors.Is(e, io.EOF) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	info, e := f.Stat()
	if e != nil {
		return false, e
	}
	offset, e := s.LogCursor(ctx, node, id)
	if e != nil {
		return false, e
	}
	if offset != info.Size() {
		return false, nil
	}
	dir := filepath.Join(filepath.Dir(path), ".ingested")
	if e = os.MkdirAll(dir, 0770); e != nil {
		return false, e
	}
	f2, e := os.CreateTemp(dir, ".cursor-")
	if e != nil {
		return false, e
	}
	defer os.Remove(f2.Name())
	_, e = f2.WriteString(strconv.FormatInt(offset, 10))
	if e == nil {
		e = f2.Sync()
	}
	f2.Close()
	if e != nil {
		return false, e
	}
	if e = os.Rename(f2.Name(), filepath.Join(dir, security.Digest(id)+".cursor")); e != nil {
		return false, e
	}
	if e = s.ArchiveComplete(ctx, node, id); e != nil {
		return false, e
	}
	directory, e := os.Open(dir)
	if e != nil {
		return false, e
	}
	defer directory.Close()
	return true, directory.Sync()
}
func scanCompressed(ctx context.Context, s *store.Store, node, path string) (bool, error) {
	f, e := os.Open(path)
	if e != nil {
		return false, e
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		return false, e
	}
	reader := bufio.NewReaderSize(contextualReader{ctx, z}, 64*1024)
	first, e := reader.ReadSlice('\n')
	if e != nil {
		z.Close()
		return false, e
	}
	firstHash := sha256.Sum256(first)
	firstID := hex.EncodeToString(firstHash[:])
	all := sha256.New()
	all.Write(first)
	rest, e := io.Copy(all, io.LimitReader(reader, maxArchiveBytes-int64(len(first))+1))
	z.Close()
	if e != nil {
		return false, e
	}
	size := int64(len(first)) + rest
	if size > maxArchiveBytes {
		return false, fmt.Errorf("decompressed log archive limit exceeded")
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	id := "gzip:" + hex.EncodeToString(all.Sum(nil))
	meta := filepath.Join(filepath.Dir(path), ".identities", strings.TrimSuffix(filepath.Base(path), ".gz")+".id")
	if b, err := os.ReadFile(meta); err == nil {
		id = strings.TrimSpace(string(b))
		if !strings.HasSuffix(id, firstID) {
			return false, fmt.Errorf("archive identity mismatch")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	} else {
		// An old compressor changed inode but preserved content. Reuse the unique
		// prior cursor identified by its first complete line; refuse ambiguous aliases.
		rows, e := s.Pool.Query(ctx, "SELECT file_id FROM mta_log_cursors WHERE node_id=$1 AND (file_id=$2 OR file_id LIKE '%:'||$2) LIMIT 2", node, firstID)
		if e != nil {
			return false, e
		}
		var aliases []string
		for rows.Next() {
			var v string
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return false, e
			}
			aliases = append(aliases, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if len(aliases) > 1 {
			return false, fmt.Errorf("ambiguous legacy archive identity")
		}
		if len(aliases) == 1 {
			id = aliases[0]
		}
	}
	offset, e := s.LogCursor(ctx, node, id)
	if e != nil {
		return false, e
	}
	if offset > size {
		return false, fmt.Errorf("archive truncated below persisted cursor")
	}
	if offset == size {
		return true, s.ArchiveComplete(ctx, node, id)
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return false, e
	}
	z, e = gzip.NewReader(f)
	if e != nil {
		return false, e
	}
	defer z.Close()
	source := contextualReader{ctx, z}
	if _, e = io.CopyN(io.Discard, source, offset); e != nil {
		return false, e
	}
	end, e := scanLogStream(ctx, s, node, id, offset, source)
	if e != nil {
		return false, e
	}
	if end != size {
		return false, fmt.Errorf("archive ends with an incomplete log line")
	}
	return true, s.ArchiveComplete(ctx, node, id)
}
