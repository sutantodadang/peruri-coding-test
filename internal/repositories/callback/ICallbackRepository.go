package callback

import (
	"context"
	"peruri/internal/apps/docproc"
)

type ICallbackRepository interface {
	SubmitCallback(ctx context.Context, cb docproc.CallbackRequest) error
	Callbacks(ctx context.Context, id string) ([]docproc.CallbackRequest, error)
}
