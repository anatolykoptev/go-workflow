package workflow

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// retryAfterErr is a test error that implements RetryAfterError.
type retryAfterErr struct {
	delay time.Duration
}

func (e *retryAfterErr) Error() string             { return fmt.Sprintf("retry after %s", e.delay) }
func (e *retryAfterErr) RetryAfter() time.Duration { return e.delay }

// TestCalculateBackoff_AppliesJitter verifies that:
//  1. All jittered values fall within [base*0.75, base*1.25].
//  2. Not all values are identical (jitter produces variation).
func TestCalculateBackoff_AppliesJitter(t *testing.T) {
	t.Parallel()

	const (
		baseMS     = 1000
		multiplier = 1.0 // no exponential growth — isolate jitter
		maxMS      = 0
		attempt    = 1
		iterations = 200
	)

	lo := int64(float64(baseMS) * 0.75)
	hi := int64(float64(baseMS) * 1.25)

	results := make(map[int64]bool)
	for range iterations {
		got := calculateBackoffWithJitter(baseMS, attempt, multiplier, maxMS)
		if got < lo || got > hi {
			t.Errorf("jittered value %d out of range [%d, %d]", got, lo, hi)
		}
		results[got] = true
	}

	// With 200 iterations and a ±25% window over 1000ms (≈500 distinct values)
	// the probability of all results being identical is astronomically small.
	if len(results) == 1 {
		t.Error("all jittered values are identical — jitter has no effect")
	}
}

// TestBackoff_HonorsRetryAfter verifies that when an error carries a
// RetryAfter hint larger than the computed backoff, the hint wins.
func TestBackoff_HonorsRetryAfter(t *testing.T) {
	t.Parallel()

	// Computed backoff: 100ms. RetryAfter hint: 10s.
	raErr := &retryAfterErr{delay: 10 * time.Second}
	computed := int64(100)

	got := retryAfterFloor(computed, raErr)
	wantMin := (10 * time.Second).Milliseconds()

	if got < wantMin {
		t.Errorf("retryAfterFloor = %d ms, want >= %d ms (Retry-After hint)", got, wantMin)
	}
}

// TestBackoff_RetryAfterDoesNotReduceDelay verifies that if the RetryAfter hint
// is smaller than the computed backoff, the computed value is kept.
func TestBackoff_RetryAfterDoesNotReduceDelay(t *testing.T) {
	t.Parallel()

	raErr := &retryAfterErr{delay: 100 * time.Millisecond}
	computed := int64(5000) // 5s computed backoff

	got := retryAfterFloor(computed, raErr)
	if got != computed {
		t.Errorf("retryAfterFloor = %d, want %d (computed backoff should not decrease)", got, computed)
	}
}

// TestBackoff_RetryAfterNilError verifies no panic when err is nil.
func TestBackoff_RetryAfterNilError(t *testing.T) {
	t.Parallel()
	got := retryAfterFloor(500, nil)
	if got != 500 {
		t.Errorf("retryAfterFloor(500, nil) = %d, want 500", got)
	}
}

// TestBackoff_RetryAfterPlainError verifies that a plain error (not implementing
// RetryAfterError) does not alter the computed delay.
func TestBackoff_RetryAfterPlainError(t *testing.T) {
	t.Parallel()
	got := retryAfterFloor(300, errors.New("plain error"))
	if got != 300 {
		t.Errorf("retryAfterFloor(300, plainErr) = %d, want 300", got)
	}
}

// TestApplyJitter_Bounds verifies the ±25% window on small values.
func TestApplyJitter_Bounds(t *testing.T) {
	t.Parallel()
	for _, base := range []int64{10, 100, 1000, 10000} {
		lo := base * 3 / 4
		hi := base*5/4 + 1 // +1 for truncation
		for range 100 {
			got := applyJitter(base)
			if got < lo || got > hi {
				t.Errorf("applyJitter(%d) = %d, out of [%d, %d]", base, got, lo, hi)
			}
		}
	}
}

// TestApplyJitter_ZeroSafe verifies zero and negative inputs are safe.
func TestApplyJitter_ZeroSafe(t *testing.T) {
	if got := applyJitter(0); got != 0 {
		t.Errorf("applyJitter(0) = %d, want 0", got)
	}
	if got := applyJitter(-1); got != -1 {
		t.Errorf("applyJitter(-1) = %d, want -1", got)
	}
}

// TestRetryAfterError_Interface verifies retryAfterErr implements the interface.
func TestRetryAfterError_Interface(t *testing.T) {
	var err error = &retryAfterErr{delay: 5 * time.Second}
	var rae RetryAfterError
	if !errors.As(err, &rae) {
		t.Fatal("retryAfterErr should implement RetryAfterError")
	}
	if rae.RetryAfter() != 5*time.Second {
		t.Errorf("RetryAfter() = %v, want 5s", rae.RetryAfter())
	}
}
