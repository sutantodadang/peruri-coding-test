package docproc

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

type DocStatus string

const (
	StatusPending    DocStatus = "PENDING"
	StatusProcessing DocStatus = "PROCESSING"
	StatusSuccess    DocStatus = "SUCCESS"
	StatusFailed     DocStatus = "FAILED"
	MaxAttempts                = 3
	MaxConcurrent              = 3
	MaxDocuments               = 1000
)

var (
	ErrBatchNotFound    = errors.New("batch not found")
	ErrDocumentNotFound = errors.New("document not found")
	ErrConflict         = errors.New("conflict")
	ErrInvalidInput     = errors.New("invalid input")
	ErrSubmitFailed     = errors.New("principal submission failed")
)

type Document struct {
	ID           string          `json:"id"`
	BatchID      string          `json:"batch_id"`
	DocumentNo   string          `json:"document_no"`
	Payload      json.RawMessage `json:"payload"`
	Status       DocStatus       `json:"status"`
	AttemptCount int             `json:"attempt_count"`
	LastError    string          `json:"last_error,omitempty"`
	RequestID    string          `json:"request_id"`
	NextRetryAt  *time.Time      `json:"next_retry_at,omitempty"`
}
type Batch struct {
	ID        string     `json:"id"`
	BatchNo   string     `json:"batch_no"`
	Documents []Document `json:"documents"`
}
type BatchStatus struct {
	BatchNo string            `json:"batch_no"`
	Status  string            `json:"status"`
	Total   int               `json:"total"`
	Counts  map[DocStatus]int `json:"counts"`
	Done    bool              `json:"done"`
}
type CallbackRequest struct {
	CallbackID   string    `json:"callback_id" validate:"required,notblank,maxbytes=128"`
	DocumentID   string    `json:"document_id" validate:"required,documentid"`
	AttemptCount int       `json:"attempt_count" validate:"min=1,max=3"`
	Status       DocStatus `json:"status" validate:"required,oneof=SUCCESS FAILED"`
	Message      string    `json:"message,omitempty" validate:"maxbytes=2048"`
}

func ValidateDocumentID(id string) error {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != id {
		return ErrInvalidInput
	}
	return nil
}

// Summarize derives batch state instead of maintaining a second mutable status.
func Summarize(no string, ds []Document) *BatchStatus {
	s := &BatchStatus{BatchNo: no, Total: len(ds), Status: "PENDING", Counts: map[DocStatus]int{StatusPending: 0, StatusProcessing: 0, StatusSuccess: 0, StatusFailed: 0}, Done: true}
	untouched := true
	for _, d := range ds {
		s.Counts[d.Status]++
		if d.AttemptCount > 0 {
			untouched = false
		}
		if d.Status == StatusPending || d.Status == StatusProcessing || (d.Status == StatusFailed && d.AttemptCount < MaxAttempts) {
			s.Done = false
		}
	}
	if s.Done {
		s.Status = "COMPLETED"
	} else if !untouched {
		s.Status = "IN_PROGRESS"
	}
	return s
}
