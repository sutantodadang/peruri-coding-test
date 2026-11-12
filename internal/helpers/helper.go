package helpers

import (
	"context"
	"errors"
	"fmt"
	"peruri/internal/apps/docproc"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func ClassifyPostgresError(e error) error {
	var pgErr *pgconn.PgError
	if errors.As(e, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("unique constraint: %w", docproc.ErrConflict)
		case "23514", "22P02", "22P05", "22001", "22021":
			return fmt.Errorf("database input: %w", docproc.ErrInvalidInput)
		}
	}
	return e
}

// Rollback also releases a transaction when its request context was canceled.
func Rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
