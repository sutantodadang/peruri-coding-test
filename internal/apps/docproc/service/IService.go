package service

import (
	"context"
	"peruri/internal/apps/docproc"
)

type IDocProcService interface {
	CreateBatch(ctx context.Context, no string, docs []docproc.Document) (*docproc.Batch, error)
	ProcessBatch(ctx context.Context, no string) error
	ProcessDocument(ctx context.Context, id string) error
	HandleCallback(ctx context.Context, cb docproc.CallbackRequest) error
	RetryFailed(ctx context.Context, no string) (int, error)
	GetBatchStatus(ctx context.Context, no string) (*docproc.BatchStatus, error)
	ListDocuments(ctx context.Context, no string) ([]docproc.Document, error)
	ListCallbacks(ctx context.Context, id string) ([]docproc.CallbackRequest, error)
	RecoverStaleDocuments(ctx context.Context) (int, error)
}
