package workflow

import (
	"errors"
	"math/rand/v2"
	"time"
)

// RetryAfterError is implemented by errors that carry a Retry-After hint.
// When the step machinery encounters this interface on a step error, it uses
// the returned duration as a floor for the retry delay (overriding the
// computed exponential backoff when larger).
type RetryAfterError interface {
	error
	RetryAfter() time.Duration
}

// applyJitter adds ±25% random variation to a millisecond delay value.
// Uses the same formula as go-kit/retry.applyJitter but operates on int64 ms.
func applyJitter(ms int64) int64 {
	if ms <= 0 {
		return ms
	}
	quarter := ms / 4 // ±25%
	// Random value in [0, 2*quarter]: subtract quarter to center around ms.
	jitter := rand.Int64N(2*quarter+1) - quarter //nolint:gosec // jitter: crypto randomness not required
	result := ms + jitter
	if result <= 0 {
		return 1
	}
	return result
}

// calculateBackoffWithJitter computes the retry delay with exponential backoff
// and ±25% jitter. Attempt is 1-based (retry 1 uses multiplier^0 = baseMS).
// This replaces direct calls to calculateBackoff at the delay-application site.
func calculateBackoffWithJitter(baseMS int64, attempt int, multiplier float64, maxMS int64) int64 {
	d := calculateBackoff(baseMS, attempt, multiplier, maxMS)
	return applyJitter(d)
}

// retryAfterFloor checks whether err implements RetryAfterError and, if so,
// returns the larger of the computed delay and the Retry-After duration.
// Returns delayMS unchanged if err does not carry a Retry-After hint.
func retryAfterFloor(delayMS int64, err error) int64 {
	if err == nil {
		return delayMS
	}
	var rae RetryAfterError
	if errors.As(err, &rae) {
		raMS := rae.RetryAfter().Milliseconds()
		if raMS > delayMS {
			return raMS
		}
	}
	return delayMS
}
