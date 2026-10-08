package store

import (
	"context"
	"fmt"

	"github.com/Elmar006/selfmail/internal/security"
)

// VerifySchema rejects stale binaries and partial/mutated migrations before a
// runtime process can accept messages or dispatch work.
func (s *Store) VerifySchema(ctx context.Context) error {
	files, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	rows, e := s.Pool.Query(ctx, "SELECT name,sha256 FROM schema_migrations")
	if e != nil {
		return e
	}
	defer rows.Close()
	recorded := map[string]string{}
	for rows.Next() {
		var name string
		var digest *string
		if e = rows.Scan(&name, &digest); e != nil {
			return e
		}
		if digest == nil {
			return fmt.Errorf("migration verification missing: %s", name)
		}
		recorded[name] = *digest
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if len(recorded) != len(files) {
		return fmt.Errorf("schema/binary migration count mismatch")
	}
	for _, file := range files {
		b, e := migrations.ReadFile("migrations/" + file.Name())
		if e != nil {
			return e
		}
		if recorded[file.Name()] != security.Digest(string(b)) {
			return fmt.Errorf("schema/binary checksum mismatch: %s", file.Name())
		}
	}
	return nil
}
