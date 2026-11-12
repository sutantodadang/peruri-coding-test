package principal

import (
	"context"

	"peruri/internal/apps/docproc"
)

type IPrincipalClient interface {
	Submit(context.Context, docproc.Document) error
}
