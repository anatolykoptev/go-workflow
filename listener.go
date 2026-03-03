package workflow

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	listenChannel = "step_done"
	eventChanSize = 64
	payloadParts  = 2
)

// StepDoneEvent is emitted when a step completes via pg_notify.
type StepDoneEvent struct {
	WorkflowID string
	StepID     string
}

// StepListener listens for PostgreSQL LISTEN/NOTIFY events on the
// "step_done" channel. Each notification carries a "workflow_id:step_id"
// payload that the Engine uses to advance the DAG.
type StepListener struct {
	connStr string
	logger  *slog.Logger
}

// NewStepListener validates the DSN by connecting once, then returns a listener.
func NewStepListener(dsn string) (*StepListener, error) {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	conn.Close(ctx)

	return &StepListener{
		connStr: dsn,
		logger:  slog.Default(),
	}, nil
}

// Listen starts a goroutine that subscribes to the "step_done" channel
// and sends parsed events to the returned channel. The channel is closed
// when ctx is cancelled or an unrecoverable error occurs.
func (l *StepListener) Listen(ctx context.Context) <-chan StepDoneEvent {
	ch := make(chan StepDoneEvent, eventChanSize)

	go l.listenLoop(ctx, ch)

	return ch
}

// Close is a no-op — each Listen call manages its own connection.
func (l *StepListener) Close() error {
	return nil
}

func (l *StepListener) listenLoop(ctx context.Context, ch chan<- StepDoneEvent) {
	defer close(ch)

	conn, err := pgx.Connect(ctx, l.connStr)
	if err != nil {
		l.logger.Error("step listener: connect", "error", err)
		return
	}
	defer conn.Close(context.Background())

	_, err = conn.Exec(ctx, "LISTEN "+listenChannel)
	if err != nil {
		l.logger.Error("step listener: LISTEN", "error", err)
		return
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled — normal shutdown
			}
			l.logger.Error("step listener: wait", "error", err)
			return
		}

		event, ok := parsePayload(notification.Payload)
		if !ok {
			l.logger.Warn("step listener: bad payload", "payload", notification.Payload)
			continue
		}

		select {
		case ch <- event:
		case <-ctx.Done():
			return
		}
	}
}

func parsePayload(payload string) (StepDoneEvent, bool) {
	parts := strings.SplitN(payload, ":", payloadParts)
	if len(parts) != payloadParts || parts[0] == "" || parts[1] == "" {
		return StepDoneEvent{}, false
	}
	return StepDoneEvent{WorkflowID: parts[0], StepID: parts[1]}, true
}

// notify sends a pg_notify on the step_done channel (used in tests).
func (l *StepListener) notify(payload string) {
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, l.connStr)
	if err != nil {
		return
	}
	defer conn.Close(ctx)

	_, _ = conn.Exec(ctx, "SELECT pg_notify('step_done', $1)", payload)
}
