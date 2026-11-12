package docproc

const (
	CallbackFutureAttempt      = "callback_future_attempt"
	CallbackRequiresProcessing = "callback_requires_processing"
	CallbackIDReused           = "callback_id_reused"
)

type CallbackConflictError struct {
	Code                string
	DocumentStatus      DocStatus
	CurrentAttemptCount int
	ConflictingFields   []string
}

func (e *CallbackConflictError) Error() string { return e.Code + ": " + e.Message() }
func (e *CallbackConflictError) Unwrap() error { return ErrConflict }

func (e *CallbackConflictError) Message() string {
	switch e.Code {
	case CallbackFutureAttempt:
		return "attempt_count exceeds the document's recorded attempt count"
	case CallbackRequiresProcessing:
		return "document is PENDING; process the document before sending a SUCCESS callback"
	case CallbackIDReused:
		return "callback_id already exists with different content; replay the original event unchanged or use a new ID for a new event"
	default:
		return "callback conflicts with the current document or event"
	}
}
