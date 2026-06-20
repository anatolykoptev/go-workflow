package workflow

import (
	"sync"
	"time"

	"github.com/anatolykoptev/go-kit/breaker"
)

// breakerRegistry holds per-endpoint circuit breakers keyed by endpoint name.
// Breakers are created on first use and reused on subsequent calls.
// Thread-safe via sync.Map. Each Engine has its own registry so breaker state
// is scoped to one Engine instance and does not bleed between parallel tests.
type breakerRegistry struct {
	m sync.Map // map[string]*breaker.Breaker
}

// get returns the breaker for the given endpoint name, creating it if absent.
// Default options: FailThreshold=5, OpenDuration=30s with 2x exponential
// backoff, MaxOpenDuration=5m, JitterPct=10, MaxHalfOpenCalls=1.
func (r *breakerRegistry) get(endpoint string) *breaker.Breaker {
	if v, ok := r.m.Load(endpoint); ok {
		return v.(*breaker.Breaker)
	}
	b := breaker.New(breaker.Options{
		Name:              endpoint,
		FailThreshold:     5,
		OpenDuration:      30 * time.Second,
		BackoffMultiplier: 2.0,
		MaxOpenDuration:   5 * time.Minute,
		JitterPct:         10,
		MaxHalfOpenCalls:  1,
	})
	// LoadOrStore avoids duplicate breakers under concurrent first-use.
	actual, _ := r.m.LoadOrStore(endpoint, b)
	return actual.(*breaker.Breaker)
}

// call wraps fn with circuit-breaker gate semantics keyed by endpoint.
//
//   - When the breaker is closed: Allow() → fn() → Record(ok).
//   - When the breaker is open: returns circuitOpenError without calling fn.
//   - When r is nil: fn is called directly (breakers disabled, used in tests
//     that construct executors without going through NewEngine).
func (r *breakerRegistry) call(endpoint string, fn func() error) error {
	if r == nil {
		return fn()
	}
	b := r.get(endpoint)
	if !b.Allow() {
		return &circuitOpenError{endpoint: endpoint}
	}
	err := fn()
	b.Record(err == nil)
	return err
}

// circuitOpenError is returned when the circuit breaker is open for an endpoint.
// Classified as transient so the engine's retry machinery backs off and retries
// once the breaker half-opens.
type circuitOpenError struct {
	endpoint string
}

func (e *circuitOpenError) Error() string {
	return "circuit open for " + e.endpoint
}

// withBreakerUsing runs fn through the given registry's circuit breaker for
// endpoint. Used in tests where the registry is explicitly provided.
func withBreakerUsing(reg *breakerRegistry, endpoint string, fn func() error) error {
	return reg.call(endpoint, fn)
}
