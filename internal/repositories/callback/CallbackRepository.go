package callback

import (
	"context"
	"errors"
	"time"

	"peruri/internal/apps/docproc"
	"peruri/internal/helpers"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CallbackRepository struct {
	pool  *pgxpool.Pool
	retry *docproc.RetryPolicy
}

var _ ICallbackRepository = (*CallbackRepository)(nil)

func NewCallbackRepository(pool *pgxpool.Pool, retry *docproc.RetryPolicy) *CallbackRepository {
	return &CallbackRepository{pool: pool, retry: retry}
}

func (r *CallbackRepository) SubmitCallback(ctx context.Context, cb docproc.CallbackRequest) error {
	if e := docproc.ValidateDocumentID(cb.DocumentID); e != nil {
		return e
	}
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer helpers.Rollback(tx)
	var status docproc.DocStatus
	var attempt int
	e = tx.QueryRow(ctx, "SELECT status,attempt_count FROM documents WHERE id=$1 FOR UPDATE", cb.DocumentID).Scan(&status, &attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return docproc.ErrDocumentNotFound
	}
	if e != nil {
		return e
	}
	if cb.AttemptCount > attempt {
		return &docproc.CallbackConflictError{Code: docproc.CallbackFutureAttempt, DocumentStatus: status, CurrentAttemptCount: attempt}
	}
	if status == docproc.StatusPending && cb.Status == docproc.StatusSuccess {
		return &docproc.CallbackConflictError{Code: docproc.CallbackRequiresProcessing, DocumentStatus: status, CurrentAttemptCount: attempt}
	}
	var insertedID int64
	e = tx.QueryRow(ctx, `INSERT INTO document_callbacks(document_id,callback_id,attempt_count,status,message)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(callback_id) DO NOTHING RETURNING id`, cb.DocumentID, cb.CallbackID, cb.AttemptCount, cb.Status, cb.Message).Scan(&insertedID)
	if errors.Is(e, pgx.ErrNoRows) {
		var old docproc.CallbackRequest
		e = tx.QueryRow(ctx, "SELECT callback_id,document_id::text,attempt_count,status,message FROM document_callbacks WHERE callback_id=$1", cb.CallbackID).Scan(&old.CallbackID, &old.DocumentID, &old.AttemptCount, &old.Status, &old.Message)
		if e != nil {
			return e
		}
		if old != cb {
			fields := make([]string, 0, 4)
			if old.DocumentID != cb.DocumentID {
				fields = append(fields, "document_id")
			}
			if old.AttemptCount != cb.AttemptCount {
				fields = append(fields, "attempt_count")
			}
			if old.Status != cb.Status {
				fields = append(fields, "status")
			}
			if old.Message != cb.Message {
				fields = append(fields, "message")
			}
			return &docproc.CallbackConflictError{Code: docproc.CallbackIDReused, DocumentStatus: status, CurrentAttemptCount: attempt, ConflictingFields: fields}
		}
		return tx.Commit(ctx)
	}
	if e != nil {
		return helpers.ClassifyPostgresError(e)
	}
	message := ""
	var nextRetryAt *time.Time
	if cb.Status == docproc.StatusFailed {
		message = cb.Message
		nextRetryAt = r.retry.Next(attempt)
	}
	_, e = tx.Exec(ctx, `UPDATE documents SET status=$3,last_error=$4,locked_at=NULL,updated_at=now(),next_retry_at=$5
  WHERE id=$1 AND status<>'SUCCESS'
  AND (($3='SUCCESS' AND $2<=attempt_count) OR ($3='FAILED' AND $2=attempt_count))
  AND (status='PROCESSING' OR (status='FAILED' AND $3='SUCCESS'))`, cb.DocumentID, cb.AttemptCount, cb.Status, message, nextRetryAt)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (r *CallbackRepository) Callbacks(ctx context.Context, id string) ([]docproc.CallbackRequest, error) {
	if e := docproc.ValidateDocumentID(id); e != nil {
		return nil, e
	}
	var exists bool
	e := r.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1)", id).Scan(&exists)
	if e != nil {
		return nil, e
	}
	if !exists {
		return nil, docproc.ErrDocumentNotFound
	}
	rows, e := r.pool.Query(ctx, "SELECT callback_id,document_id::text,attempt_count,status,message FROM document_callbacks WHERE document_id=$1 ORDER BY id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]docproc.CallbackRequest, 0)
	for rows.Next() {
		var cb docproc.CallbackRequest
		if e = rows.Scan(&cb.CallbackID, &cb.DocumentID, &cb.AttemptCount, &cb.Status, &cb.Message); e != nil {
			return nil, e
		}
		out = append(out, cb)
	}
	return out, rows.Err()
}
