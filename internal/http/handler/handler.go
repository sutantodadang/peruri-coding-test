package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"peruri/internal/apps/docproc"
	"peruri/internal/apps/docproc/service"
	"peruri/internal/http/middlewares"
	"peruri/internal/http/response"

	"github.com/go-playground/validator/v10"
)

type DocProcHandler struct {
	service  service.IDocProcService
	validate *validator.Validate
}

var _ IDocProcHandler = (*DocProcHandler)(nil)

func NewDocProcHandler(service service.IDocProcService, validate *validator.Validate) *DocProcHandler {
	return &DocProcHandler{service: service, validate: validate}
}

func (h *DocProcHandler) ProcessBatch(w http.ResponseWriter, r *http.Request) {
	err := h.service.ProcessBatch(r.Context(), r.PathValue("no"))
	finish(w, r, map[string]string{"message": "submission finished; accepted documents await callbacks"}, err)
}
func (h *DocProcHandler) ProcessDocument(w http.ResponseWriter, r *http.Request) {
	err := h.service.ProcessDocument(r.Context(), r.PathValue("id"))
	finish(w, r, map[string]string{"message": "document processed or skipped"}, err)
}
func (h *DocProcHandler) RetryFailed(w http.ResponseWriter, r *http.Request) {
	n, err := h.service.RetryFailed(r.Context(), r.PathValue("no"))
	finish(w, r, map[string]int{"retried": n}, err)
}
func (h *DocProcHandler) GetBatchStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.GetBatchStatus(r.Context(), r.PathValue("no"))
	finish(w, r, status, err)
}
func (h *DocProcHandler) ListDocuments(w http.ResponseWriter, r *http.Request) {
	documents, err := h.service.ListDocuments(r.Context(), r.PathValue("no"))
	finish(w, r, documents, err)
}
func (h *DocProcHandler) ListCallbacks(w http.ResponseWriter, r *http.Request) {
	callbacks, err := h.service.ListCallbacks(r.Context(), r.PathValue("id"))
	finish(w, r, callbacks, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		middlewares.RequestLogger(r.Context()).WarnContext(r.Context(), "request rejected", "reason", "unsupported_content_type")
		response.JSON(w, 415, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	e := d.Decode(v)
	if e == nil {
		if trailingErr := d.Decode(new(any)); trailingErr != io.EOF {
			e = trailingErr
			if e == nil {
				e = docproc.ErrInvalidInput
			}
		}
	}
	if e != nil {
		status := 400
		var tooBig *http.MaxBytesError
		if errors.As(e, &tooBig) {
			status = 413
		}
		middlewares.RequestLogger(r.Context()).WarnContext(r.Context(), "request rejected", "reason", "invalid_json", "status", status)
		response.JSON(w, status, map[string]string{"error": "invalid JSON request"})
		return false
	}
	return true
}
func (h *DocProcHandler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	var in createBatchRequest
	if !decode(w, r, &in) || !h.validateRequest(w, r, in) {
		return
	}
	docs := make([]docproc.Document, len(in.Documents))
	for i, d := range in.Documents {
		docs[i] = docproc.Document{DocumentNo: d.DocumentNo, Payload: d.Payload}
	}
	b, e := h.service.CreateBatch(r.Context(), in.BatchNo, docs)
	if e != nil {
		finish(w, r, nil, e)
		return
	}
	response.JSON(w, 201, b)
}
func (h *DocProcHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	var cb docproc.CallbackRequest
	if !decode(w, r, &cb) || !h.validateRequest(w, r, cb) {
		return
	}
	e := h.service.HandleCallback(r.Context(), cb)
	attrs := []slog.Attr{
		slog.String("callback_id", cb.CallbackID), slog.String("document_id", cb.DocumentID),
		slog.Int("attempt_count", cb.AttemptCount), slog.String("callback_status", string(cb.Status)),
	}
	if e == nil {
		middlewares.RequestLogger(r.Context()).LogAttrs(r.Context(), slog.LevelInfo, "callback acknowledged", attrs...)
	}
	finish(w, r, map[string]string{"message": "callback acknowledged"}, e, attrs...)
}
func finish(w http.ResponseWriter, r *http.Request, v any, e error, attrs ...slog.Attr) {
	if e == nil {
		response.JSON(w, 200, v)
		return
	}
	status, message := 500, "internal error"
	switch {
	case errors.Is(e, docproc.ErrInvalidInput):
		status, message = 400, "invalid input"
	case errors.Is(e, docproc.ErrBatchNotFound), errors.Is(e, docproc.ErrDocumentNotFound):
		status, message = 404, "resource not found"
	case errors.Is(e, docproc.ErrConflict):
		status, message = 409, "conflict"
	case errors.Is(e, context.DeadlineExceeded):
		status, message = 504, "operation timed out; inspect document status before retry"
	case errors.Is(e, context.Canceled):
		status, message = 408, "operation canceled"
	case errors.Is(e, docproc.ErrSubmitFailed):
		status, message = 502, "principal submission failed; inspect documents for details"
	}
	level, event := slog.LevelWarn, "operation failed"
	if status >= 500 {
		level = slog.LevelError
	}
	body := map[string]any{"error": message}
	var conflict *docproc.CallbackConflictError
	if status == http.StatusConflict && errors.As(e, &conflict) {
		event = "callback rejected"
		attrs = append(attrs, slog.String("reason", conflict.Code), slog.String("document_status", string(conflict.DocumentStatus)), slog.Int("current_attempt_count", conflict.CurrentAttemptCount))
		body["code"], body["message"] = conflict.Code, conflict.Message()
		body["document_status"], body["current_attempt_count"] = conflict.DocumentStatus, conflict.CurrentAttemptCount
		body["request_id"] = w.Header().Get("X-Request-ID")
		if len(conflict.ConflictingFields) > 0 {
			attrs = append(attrs, slog.Any("conflicting_fields", conflict.ConflictingFields))
			body["conflicting_fields"] = conflict.ConflictingFields
		}
	}
	attrs = append(attrs, slog.Int("status", status), slog.Any("error", e))
	middlewares.RequestLogger(r.Context()).LogAttrs(r.Context(), level, event, attrs...)
	response.JSON(w, status, body)
}
