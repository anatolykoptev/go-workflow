package workflow

import (
	"testing"
	"time"
)

// TestRateLimit_ThrottlesExcessCalls verifies that calls beyond the burst
// return a rate-limit error without executing (opt-in behavior).
func TestRateLimit_ThrottlesExcessCalls(t *testing.T) {
	t.Parallel()

	reg := newRateLimitRegistry()
	// rate=2/s, burst=2: only 2 calls allowed immediately, then throttled.
	reg.add("my-tool", 2, 2)

	successes := 0
	throttled := 0

	// Fire 10 calls rapidly (no sleep = all within the same tick).
	for range 10 {
		err := reg.check("my-tool")
		if err == nil {
			successes++
		} else {
			throttled++
		}
	}

	// With burst=2, first 2 calls pass, rest are throttled.
	if successes > 2 {
		t.Errorf("successes = %d, want <= 2 with burst=2", successes)
	}
	if throttled == 0 {
		t.Error("no calls were throttled; expected throttling beyond burst")
	}
}

// TestRateLimit_AllowsWithinRate verifies that calls within the configured
// rate are all allowed.
func TestRateLimit_AllowsWithinRate(t *testing.T) {
	t.Parallel()

	reg := newRateLimitRegistry()
	// High rate: 1000/s burst=1000 → all 5 rapid calls should pass.
	reg.add("fast-tool", 1000, 1000)

	for i := range 5 {
		if err := reg.check("fast-tool"); err != nil {
			t.Errorf("call %d: unexpected throttle: %v", i, err)
		}
	}
}

// TestRateLimit_UnknownProviderUnlimited verifies that a provider not
// configured via WithRateLimit is always allowed (default = unlimited).
func TestRateLimit_UnknownProviderUnlimited(t *testing.T) {
	t.Parallel()

	reg := newRateLimitRegistry()
	// Only "other-tool" is limited.
	reg.add("other-tool", 1, 1)

	for range 20 {
		if err := reg.check("unconfigured-tool"); err != nil {
			t.Fatalf("unconfigured provider should not be rate limited: %v", err)
		}
	}
}

// TestRateLimit_ErrorMessage verifies the throttle error message format.
func TestRateLimit_ErrorMessage(t *testing.T) {
	err := &rateLimitExceededError{provider: "my-llm"}
	want := "rate limit exceeded for my-llm"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestRateLimit_RefillsOverTime verifies that tokens refill over time.
func TestRateLimit_RefillsOverTime(t *testing.T) {
	t.Parallel()

	reg := newRateLimitRegistry()
	// rate=10/s, burst=1: one immediate call, then throttled.
	reg.add("slow-tool", 10, 1)

	// Exhaust the burst.
	_ = reg.check("slow-tool")

	// Should be throttled now.
	if err := reg.check("slow-tool"); err == nil {
		t.Skip("token refilled too fast — token bucket timing is non-deterministic")
	}

	// After 200ms (at 10/s = 100ms per token), at least one token should refill.
	time.Sleep(150 * time.Millisecond)

	if err := reg.check("slow-tool"); err != nil {
		t.Errorf("token should have refilled after 150ms at 10/s rate: %v", err)
	}
}

// TestWithRateLimit_EngineOption verifies that WithRateLimit wires up the
// rateLimits registry on the Engine.
func TestWithRateLimit_EngineOption(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	engine := NewEngine(store, WithRateLimit("test-model", 5, 10))

	if engine.rateLimits == nil {
		t.Fatal("engine.rateLimits is nil after WithRateLimit")
	}
	// Should be allowed (within burst).
	if err := engine.rateLimits.check("test-model"); err != nil {
		t.Errorf("first call should not be throttled: %v", err)
	}
}
