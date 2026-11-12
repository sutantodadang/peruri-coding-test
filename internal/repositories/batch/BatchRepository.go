package batch

import (
	"context"
	"errors"
	"time"

	"peruri/internal/apps/docproc"
	"peruri/internal/helpers"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type BatchRepository struct {
	pool  *pgxpool.Pool
	retry *docproc.RetryPolicy
}

var _ IBatchRepository = (*BatchRepository)(nil)

func NewBatchRepository(pool *pgxpool.Pool, retry *docproc.RetryPolicy) *BatchRepository {
	return &BatchRepository{pool: pool, retry: retry}
}

type scanner interface{ Scan(...any) error }

const columns = "id::text,batch_id::text,document_no,payload,status,attempt_count,last_error,request_id::text,next_retry_at"

func scanDocument(row scanner) (docproc.Document, error) {
	var d docproc.Document
	e := row.Scan(&d.ID, &d.BatchID, &d.DocumentNo, &d.Payload, &d.Status, &d.AttemptCount, &d.LastError, &d.RequestID, &d.NextRetryAt)
	return d, e
}

func (r *BatchRepository) CreateBatch(ctx context.Context, no string, docs []docproc.Document) (*docproc.Batch, error) {
	tx, e := r.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer helpers.Rollback(tx)
	b := &docproc.Batch{BatchNo: no, Documents: make([]docproc.Document, 0, len(docs))}
	e = tx.QueryRow(ctx, "INSERT INTO batches(batch_no,total_documents) VALUES($1,$2) RETURNING id::text", no, len(docs)).Scan(&b.ID)
	if e != nil {
		return nil, helpers.ClassifyPostgresError(e)
	}
	for _, d := range docs {
		stored, e := scanDocument(tx.QueryRow(ctx, "INSERT INTO documents(batch_id,document_no,payload) VALUES($1,$2,$3) RETURNING "+columns, b.ID, d.DocumentNo, d.Payload))
		if e != nil {
			return nil, helpers.ClassifyPostgresError(e)
		}
		b.Documents = append(b.Documents, stored)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return b, nil
}

func (r *BatchRepository) ListDocuments(ctx context.Context, no string) ([]docproc.Document, error) {
	rows, e := r.pool.Query(ctx, "SELECT "+columns+" FROM documents WHERE batch_id=(SELECT id FROM batches WHERE batch_no=$1) ORDER BY documents.id", no)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ds := make([]docproc.Document, 0)
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return nil, e
		}
		ds = append(ds, d)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if len(ds) == 0 {
		return nil, docproc.ErrBatchNotFound
	}
	return ds, nil
}

func (r *BatchRepository) Claim(ctx context.Context, id string) (docproc.Document, bool, error) {
	if e := docproc.ValidateDocumentID(id); e != nil {
		return docproc.Document{}, false, e
	}
	d, e := scanDocument(r.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT id FROM documents WHERE id=$1 AND status='PENDING' AND attempt_count<max_attempts FOR UPDATE SKIP LOCKED
 ) UPDATE documents SET status='PROCESSING',attempt_count=attempt_count+1,
	 locked_at=now(),updated_at=now(),last_error='',next_retry_at=NULL WHERE id IN (SELECT id FROM candidate) RETURNING `+columns, id))
	if errors.Is(e, pgx.ErrNoRows) {
		var exists bool
		e = r.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1)", id).Scan(&exists)
		if e != nil {
			return d, false, e
		}
		if !exists {
			return d, false, docproc.ErrDocumentNotFound
		}
		return d, false, nil
	}
	return d, e == nil, e
}

func (r *BatchRepository) SubmitError(ctx context.Context, id string, attempt int, message string) error {
	_, e := r.pool.Exec(ctx, "UPDATE documents SET status='FAILED',last_error=$3,locked_at=NULL,updated_at=now(),next_retry_at=$4 WHERE id=$1 AND attempt_count=$2 AND status='PROCESSING'", id, attempt, message, r.retry.Next(attempt))
	return e
}

func (r *BatchRepository) RetryFailed(ctx context.Context, no string) (int, error) {
	var id string
	e := r.pool.QueryRow(ctx, "SELECT id::text FROM batches WHERE batch_no=$1", no).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return 0, docproc.ErrBatchNotFound
	}
	if e != nil {
		return 0, e
	}
	result, e := r.pool.Exec(ctx, "UPDATE documents SET status='PENDING',next_retry_at=NULL,updated_at=now() WHERE batch_id=$1 AND status='FAILED' AND attempt_count<max_attempts AND next_retry_at<=$2", id, r.retry.Now())
	return int(result.RowsAffected()), e
}

func (r *BatchRepository) Recover(ctx context.Context, before time.Time) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer helpers.Rollback(tx)
	rows, err := tx.Query(ctx, "SELECT id::text,attempt_count FROM documents WHERE status='PROCESSING' AND locked_at<$1 ORDER BY documents.id LIMIT 100 FOR UPDATE SKIP LOCKED", before)
	if err != nil {
		return 0, err
	}
	var updates pgx.Batch
	for rows.Next() {
		var id string
		var attempt int
		if err = rows.Scan(&id, &attempt); err != nil {
			rows.Close()
			return 0, err
		}
		updates.Queue("UPDATE documents SET status='FAILED',last_error='processing lease expired',locked_at=NULL,updated_at=now(),next_retry_at=$2 WHERE id=$1", id, r.retry.Next(attempt))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	results := tx.SendBatch(ctx, &updates)
	if err = results.Close(); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return updates.Len(), nil
}
