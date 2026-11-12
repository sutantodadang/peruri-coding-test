package handler

import "net/http"

type IDocProcHandler interface {
	CreateBatch(http.ResponseWriter, *http.Request)
	ProcessBatch(http.ResponseWriter, *http.Request)
	ProcessDocument(http.ResponseWriter, *http.Request)
	HandleCallback(http.ResponseWriter, *http.Request)
	RetryFailed(http.ResponseWriter, *http.Request)
	GetBatchStatus(http.ResponseWriter, *http.Request)
	ListDocuments(http.ResponseWriter, *http.Request)
	ListCallbacks(http.ResponseWriter, *http.Request)
}
