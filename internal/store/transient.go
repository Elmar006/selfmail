package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Elmar006/selfmail/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func transient(e error) error {
	if e == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) || pgconn.SafeToRetry(e) {
		return fmt.Errorf("persistence temporarily unavailable: %w", domain.ErrUnavailable)
	}
	if errors.As(e, &pg) {
		switch pg.Code {
		case "53100", "53200", "53300", "53400", "55P03", "40P01", "40001", "57014", "57P01", "57P02", "57P03", "58030":
			return fmt.Errorf("persistence SQLSTATE %s: %w", pg.Code, domain.ErrUnavailable)
		}
	}
	return e
}
