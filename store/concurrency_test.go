package store_test

import (
	"os"
	"testing"

	"github.com/anatolykoptev/go-workflow/store"
)

func newTestLimiter(t *testing.T) *store.ConcurrencyLimiter {
	t.Helper()

	dsn := os.Getenv("WORKFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WORKFLOW_TEST_POSTGRES_DSN not set")
	}

	cl, err := store.NewConcurrencyLimiter(dsn)
	if err != nil {
		t.Fatalf("NewConcurrencyLimiter: %v", err)
	}
	t.Cleanup(func() { cl.Close() })

	return cl
}

func TestConcurrencyLimiter_KindLimit(t *testing.T) {
	cl := newTestLimiter(t)
	key := "kind:llm"

	if err := cl.SetLimit(key, 2); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	t.Cleanup(func() { cl.Release(key); cl.Release(key) })

	// First two acquires should succeed.
	if !cl.TryAcquire(key) {
		t.Fatal("first TryAcquire should succeed")
	}
	if !cl.TryAcquire(key) {
		t.Fatal("second TryAcquire should succeed")
	}

	// Third acquire should fail — at capacity.
	if cl.TryAcquire(key) {
		t.Fatal("third TryAcquire should fail (at capacity)")
	}

	// Release one slot, then acquire should succeed again.
	cl.Release(key)

	if !cl.TryAcquire(key) {
		t.Fatal("TryAcquire after Release should succeed")
	}
}

func TestConcurrencyLimiter_EntityKey(t *testing.T) {
	cl := newTestLimiter(t)
	key := "entity:owner:123"

	if err := cl.SetLimit(key, 1); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	t.Cleanup(func() { cl.Release(key) })

	// First acquire should succeed.
	if !cl.TryAcquire(key) {
		t.Fatal("first TryAcquire should succeed")
	}

	// Second acquire should fail — limit is 1.
	if cl.TryAcquire(key) {
		t.Fatal("second TryAcquire should fail (limit 1)")
	}
}
