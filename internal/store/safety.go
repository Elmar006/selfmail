package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/Elmar006/selfmail/internal/security"
	"github.com/jackc/pgx/v5"
)

func (s *Store) DeliveryPermit(ctx context.Context) (func(), error) {
	if s.Control == nil {
		if s.Journal != nil {
			if e := s.Journal.CheckHead(ctx); e != nil {
				return nil, e
			}
		}
		return func() {}, nil
	}
	p, e := s.Control.Acquire(ctx)
	if e != nil {
		return nil, fmt.Errorf("dispatch fenced: %w", domain.ErrUnavailable)
	}
	if s.Journal != nil {
		if e = s.Journal.CheckHead(ctx); e != nil {
			p.Close()
			return nil, fmt.Errorf("journal not ready: %w", domain.ErrUnavailable)
		}
	}
	return p.Close, nil
}
func (s *Store) CheckAdmission(ctx context.Context) error {
	release, e := s.DeliveryPermit(ctx)
	if e != nil {
		return e
	}
	release()
	return s.Pool.Ping(ctx)
}
func (s *Store) VerifyVault(ctx context.Context, vault *security.Vault, initialize bool) error {
	const proof = "selfmail:master-key:v1"
	var cipher []byte
	e := s.Pool.QueryRow(ctx, "SELECT check_cipher FROM crypto_guard WHERE id").Scan(&cipher)
	if errors.Is(e, pgx.ErrNoRows) && initialize {
		// Validate existing encrypted material before binding a new guard.
		rows, e := s.Pool.Query(ctx, "SELECT encrypted_key,'dkim:'||id::text FROM sender_domains UNION ALL SELECT encrypted_secret,'webhook:'||id::text FROM webhook_endpoints")
		if e != nil {
			return e
		}
		for rows.Next() {
			var data []byte
			var label string
			if e = rows.Scan(&data, &label); e != nil {
				rows.Close()
				return e
			}
			if _, e = vault.Open(data, label); e != nil {
				rows.Close()
				return fmt.Errorf("existing encryption key mismatch: %w", e)
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		cipher, e = vault.Seal([]byte(proof), proof)
		if e != nil {
			return e
		}
		if _, e = s.Pool.Exec(ctx, "INSERT INTO crypto_guard(id,check_cipher) VALUES(true,$1) ON CONFLICT DO NOTHING", cipher); e != nil {
			return e
		}
		return s.VerifyVault(ctx, vault, false)
	}
	if e != nil {
		return fmt.Errorf("master key guard unavailable: %w", e)
	}
	plain, e := vault.Open(cipher, proof)
	if e != nil || string(plain) != proof {
		return fmt.Errorf("MASTER_KEY does not match database guard")
	}
	return nil
}
