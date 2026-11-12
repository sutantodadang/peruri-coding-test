package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/principal"
	"peruri/internal/repositories/batch"
	"peruri/internal/repositories/callback"
	"strings"
	"sync"
	"time"
)

type DocProcService struct {
	batchRepo     batch.IBatchRepository
	callbackRepo  callback.ICallbackRepository
	client        principal.IPrincipalClient
	slots         chan struct{}
	submitTimeout time.Duration
}

const (
	processingLease = 60 * time.Second
	cleanupTimeout  = 3 * time.Second
)

var _ IDocProcService = (*DocProcService)(nil)

func NewDocProcService(batchRepo batch.IBatchRepository, callbackRepo callback.ICallbackRepository, client principal.IPrincipalClient, submitTimeout time.Duration) (*DocProcService, error) {
	if batchRepo == nil || callbackRepo == nil || client == nil || submitTimeout <= 0 || submitTimeout >= processingLease-cleanupTimeout {
		return nil, fmt.Errorf("service dependencies or submit timeout: %w", docproc.ErrInvalidInput)
	}
	return &DocProcService{batchRepo: batchRepo, callbackRepo: callbackRepo, client: client, slots: make(chan struct{}, docproc.MaxConcurrent), submitTimeout: submitTimeout}, nil
}
func (s *DocProcService) CreateBatch(ctx context.Context, no string, docs []docproc.Document) (*docproc.Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(no) == "" || len(no) > 64 || len(docs) == 0 || len(docs) > docproc.MaxDocuments {
		return nil, fmt.Errorf("create batch: %w", docproc.ErrInvalidInput)
	}
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.DocumentNo) == "" || len(d.DocumentNo) > 64 || seen[d.DocumentNo] || !json.Valid(d.Payload) || string(d.Payload) == "null" {
			return nil, fmt.Errorf("create document: %w", docproc.ErrInvalidInput)
		}
		seen[d.DocumentNo] = true
	}
	b, err := s.batchRepo.CreateBatch(ctx, no, docs)
	if err != nil {
		return nil, fmt.Errorf("create batch %s: %w", no, err)
	}
	return b, nil
}
func (s *DocProcService) ProcessBatch(ctx context.Context, no string) error {
	ds, err := s.batchRepo.ListDocuments(ctx, no)
	if err != nil {
		return fmt.Errorf("process batch: %w", err)
	}
	jobs := make(chan string, len(ds))
	for _, d := range ds {
		if d.Status == docproc.StatusPending {
			jobs <- d.ID
		}
	}
	close(jobs)
	errs := make(chan error, len(ds))
	var workers sync.WaitGroup
	for range docproc.MaxConcurrent {
		workers.Go(func() {
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := s.ProcessDocument(ctx, id); err != nil {
					errs <- err
				}
			}
		})
	}
	workers.Wait()
	close(errs)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("process batch: %w", err)
	}
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}
func (s *DocProcService) ProcessDocument(ctx context.Context, id string) error {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slots }()
	if err := ctx.Err(); err != nil {
		return err
	}
	d, claimed, err := s.batchRepo.Claim(ctx, id)
	if err != nil {
		return fmt.Errorf("claim document: %w", err)
	}
	if !claimed {
		return nil
	}
	submitCtx, cancel := context.WithTimeout(ctx, s.submitTimeout)
	defer cancel()
	err = s.client.Submit(submitCtx, d)
	if err == nil {
		return nil
	}
	// Cancellation must not prevent persisting the outcome of an already claimed attempt.
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer stop()
	persistErr := s.batchRepo.SubmitError(cleanup, d.ID, d.AttemptCount, err.Error())
	if persistErr != nil {
		return errors.Join(fmt.Errorf("submit document %s: %w", id, err), fmt.Errorf("persist failure: %w", persistErr))
	}
	return fmt.Errorf("submit document %s: %w: %w", id, docproc.ErrSubmitFailed, err)
}
func (s *DocProcService) HandleCallback(ctx context.Context, cb docproc.CallbackRequest) error {
	if strings.TrimSpace(cb.CallbackID) == "" || len(cb.CallbackID) > 128 || cb.DocumentID == "" || cb.AttemptCount < 1 || cb.AttemptCount > docproc.MaxAttempts || (cb.Status != docproc.StatusSuccess && cb.Status != docproc.StatusFailed) || len(cb.Message) > 2048 {
		return fmt.Errorf("callback: %w", docproc.ErrInvalidInput)
	}
	if err := s.callbackRepo.SubmitCallback(ctx, cb); err != nil {
		return fmt.Errorf("callback: %w", err)
	}
	return nil
}
func (s *DocProcService) RetryFailed(ctx context.Context, no string) (int, error) {
	n, err := s.batchRepo.RetryFailed(ctx, no)
	if err != nil {
		return 0, fmt.Errorf("retry batch: %w", err)
	}
	return n, nil
}
func (s *DocProcService) GetBatchStatus(ctx context.Context, no string) (*docproc.BatchStatus, error) {
	ds, err := s.batchRepo.ListDocuments(ctx, no)
	if err != nil {
		return nil, fmt.Errorf("batch status: %w", err)
	}
	return docproc.Summarize(no, ds), nil
}

func (s *DocProcService) ListDocuments(ctx context.Context, no string) ([]docproc.Document, error) {
	ds, err := s.batchRepo.ListDocuments(ctx, no)
	if err != nil {
		return nil, fmt.Errorf("list batch documents: %w", err)
	}
	return ds, nil
}

func (s *DocProcService) ListCallbacks(ctx context.Context, id string) ([]docproc.CallbackRequest, error) {
	callbacks, err := s.callbackRepo.Callbacks(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list document callbacks: %w", err)
	}
	return callbacks, nil
}

func (s *DocProcService) RecoverStaleDocuments(ctx context.Context) (int, error) {
	n, err := s.batchRepo.Recover(ctx, time.Now().Add(-processingLease))
	if err != nil {
		return 0, fmt.Errorf("recover processing leases: %w", err)
	}
	return n, nil
}
