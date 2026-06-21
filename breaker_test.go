package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
)

// freshBreaker creates a circuit breaker with tight thresholds for fast tests.
func freshBreaker(name string, failThreshold uint32, openDuration time.Duration) *breaker.Breaker {
	return breaker.New(breaker.Options{
		Name:             name,
		FailThreshold:    failThreshold,
		OpenDuration:     openDuration,
		MaxHalfOpenCalls: 1,
	})
}

// TestBreaker_OpensAfterThreshold verifies that after FailThreshold consecutive
// failures the breaker opens and subsequent Allow() calls return false without
// executing the action.
func TestBreaker_OpensAfterThreshold(t *testing.T) {
	t.Parallel()

	// Isolated registry so tests don't share state.
	reg := &breakerRegistry{}
	// Inject a tight breaker: opens after 3 consecutive failures.
	b := freshBreaker("test-endpoint", 3, 10*time.Second)
	reg.m.Store("test-endpoint", b)

	callCount := 0
	mockFn := func() error {
		callCount++
		return errors.New("downstream failure")
	}

	// Execute 3 failures → should trip the breaker.
	for range 3 {
		_ = withBreakerUsing(reg, "test-endpoint", mockFn)
	}

	if b.State() != breaker.StateOpen {
		t.Fatalf("breaker state = %s, want open after %d failures", b.State(), 3)
	}

	// Next call must return circuitOpenError WITHOUT incrementing callCount.
	prevCount := callCount
	err := withBreakerUsing(reg, "test-endpoint", mockFn)
	if err == nil {
		t.Fatal("expected circuitOpenError, got nil")
	}
	var coe *circuitOpenError
	if !errors.As(err, &coe) {
		t.Fatalf("err type = %T, want *circuitOpenError", err)
	}
	if callCount != prevCount {
		t.Errorf("callCount advanced to %d; fn must not be called when circuit is open", callCount)
	}
}

// TestBreaker_RecoversAfterHalfOpen verifies the recovery cycle:
// open → (cooldown expires) → half-open → probe succeeds → closed.
func TestBreaker_RecoversAfterHalfOpen(t *testing.T) {
	t.Parallel()

	reg := &breakerRegistry{}
	// Very short open duration so the test doesn't wait.
	b := freshBreaker("recover-endpoint", 2, 10*time.Millisecond)
	reg.m.Store("recover-endpoint", b)

	// Trip the breaker.
	failFn := func() error { return errors.New("failure") }
	for range 2 {
		_ = withBreakerUsing(reg, "recover-endpoint", failFn)
	}
	if b.State() != breaker.StateOpen {
		t.Fatal("breaker must be open after 2 failures")
	}

	// Wait for cooldown to expire (10ms).
	time.Sleep(25 * time.Millisecond)

	// Probe with success → breaker should close.
	successFn := func() error { return nil }
	err := withBreakerUsing(reg, "recover-endpoint", successFn)
	if err != nil {
		t.Fatalf("probe call failed: %v", err)
	}

	if b.State() != breaker.StateClosed {
		t.Errorf("breaker state = %s, want closed after successful probe", b.State())
	}
}


// TestBreaker_CircuitOpenError_Message checks the error message format.
func TestBreaker_CircuitOpenError_Message(t *testing.T) {
	err := &circuitOpenError{endpoint: "my-service"}
	want := "circuit open for my-service"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestBreaker_WithBreaker_PassesOnSuccess confirms withBreaker returns nil when
// fn succeeds and does not break the circuit.
func TestBreaker_WithBreaker_PassesOnSuccess(t *testing.T) {
	t.Parallel()
	reg := &breakerRegistry{}
	b := freshBreaker("ok-endpoint", 5, 10*time.Second)
	reg.m.Store("ok-endpoint", b)

	for range 10 {
		err := withBreakerUsing(reg, "ok-endpoint", func() error { return nil })
		if err != nil {
			t.Fatalf("unexpected error on success call: %v", err)
		}
	}
	if b.State() != breaker.StateClosed {
		t.Errorf("breaker = %s, want closed after 10 successes", b.State())
	}
}

// TestCircuitOpenError_IsTransient verifies that a circuit-open error is
// recognised as transient by IsTransientError, so the auto-retry watchdog
// (AutoRetryFailed) picks up workflows that failed due to an open circuit.
//
// Red-on-revert: if "circuit open" is removed from transientPatterns, this
// test fails. Without it, AutoRetryFailed classifies circuit-open failures
// as permanent and never re-queues them, defeating circuit recovery.
func TestCircuitOpenError_IsTransient(t *testing.T) {
	t.Parallel()

	err := &circuitOpenError{endpoint: "llm:claude-3-5-sonnet"}
	if !IsTransientError(err.Error()) {
		t.Errorf("IsTransientError(%q) = false, want true; circuit-open errors must be transient so AutoRetryFailed re-queues them", err.Error())
	}
}
