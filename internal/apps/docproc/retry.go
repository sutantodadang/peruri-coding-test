package docproc

import "time"

type RetryPolicy struct {
	now    func() time.Time
	sample func() float64
}

// Sources must be concurrency-safe; sample returns a value in [0,1).
func NewRetryPolicy(now func() time.Time, sample func() float64) *RetryPolicy {
	return &RetryPolicy{now: now, sample: sample}
}

func (p *RetryPolicy) Now() time.Time { return p.now().UTC() }

// Equal jitter: half the exponential window plus a random amount in its upper half.
func (p *RetryPolicy) Next(attempt int) *time.Time {
	if attempt < 1 || attempt >= MaxAttempts {
		return nil
	}
	window := time.Second << attempt
	delay := window/2 + time.Duration(p.sample()*float64(window/2))
	next := p.Now().Add(delay)
	return &next
}
