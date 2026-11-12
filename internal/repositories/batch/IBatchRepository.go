package batch

import (
	"context"
	"peruri/internal/apps/docproc"
	"time"
)

type IBatchRepository interface {
	CreateBatch(ctx context.Context, no string, docs []docproc.Document) (*docproc.Batch, error)
	ListDocuments(ctx context.Context, no string) ([]docproc.Document, error)
	RetryFailed(ctx context.Context, no string) (int, error)
	Recover(ctx context.Context, before time.Time) (int, error)
	Claim(ctx context.Context, id string) (docproc.Document, bool, error)
	SubmitError(ctx context.Context, id string, attempt int, message string) error
}
