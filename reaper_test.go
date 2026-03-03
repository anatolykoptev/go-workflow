package workflow

import (
	"context"
	"testing"
	"time"
)

type mockReaper struct {
	reaped int
	err    error
}

func (m *mockReaper) ReapStale(_ time.Duration) (int, error) {
	return m.reaped, m.err
}

func TestReaper_RunAndStop(t *testing.T) {
	mock := &mockReaper{reaped: 2}
	r := NewReaper(mock, time.Minute, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	r.Run(ctx)
	// Just verify it runs and stops cleanly without blocking.
}
