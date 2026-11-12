package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"
	"peruri/internal/apps/docproc"
	"peruri/internal/http/middlewares"
	"peruri/internal/http/response"
)

type createBatchRequest struct {
	BatchNo   string                  `json:"batch_no" validate:"required,notblank,maxbytes=64"`
	Documents []createDocumentRequest `json:"documents" validate:"required,min=1,max=1000,unique=DocumentNo,dive"`
}

type createDocumentRequest struct {
	DocumentNo string          `json:"document_no" validate:"required,notblank,maxbytes=64"`
	Payload    json.RawMessage `json:"payload" validate:"required,payload"`
}

func NewRequestValidator() (*validator.Validate, error) {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		return strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
	})
	for name, rule := range map[string]validator.Func{
		"notblank": func(field validator.FieldLevel) bool {
			return strings.TrimSpace(field.Field().String()) != ""
		},
		"maxbytes": func(field validator.FieldLevel) bool {
			limit, err := strconv.Atoi(field.Param())
			return err == nil && len(field.Field().String()) <= limit
		},
		"payload": func(field validator.FieldLevel) bool {
			value := field.Field().Bytes()
			return json.Valid(value) && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
		},
		"documentid": func(field validator.FieldLevel) bool {
			return docproc.ValidateDocumentID(field.Field().String()) == nil
		},
	} {
		if err := v.RegisterValidation(name, rule); err != nil {
			return nil, fmt.Errorf("register %s validation: %w", name, err)
		}
	}
	return v, nil
}

type fieldError struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
	Limit string `json:"limit,omitempty"`
}

func (h *DocProcHandler) validateRequest(w http.ResponseWriter, r *http.Request, input any) bool {
	err := h.validate.Struct(input)
	if err == nil {
		return true
	}
	var failures validator.ValidationErrors
	if !errors.As(err, &failures) {
		finish(w, r, nil, err)
		return false
	}
	fields := make([]fieldError, 0, len(failures))
	for _, failure := range failures {
		_, field, _ := strings.Cut(failure.Namespace(), ".")
		fields = append(fields, fieldError{Field: field, Rule: failure.Tag(), Limit: failure.Param()})
	}
	middlewares.RequestLogger(r.Context()).WarnContext(r.Context(), "request validation failed", "fields", fields)
	response.JSON(w, http.StatusBadRequest, struct {
		Error  string       `json:"error"`
		Fields []fieldError `json:"fields"`
	}{Error: "validation failed", Fields: fields})
	return false
}
