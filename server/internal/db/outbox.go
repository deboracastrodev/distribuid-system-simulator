package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// OutboxEntry is a notification waiting to be delivered.
type OutboxEntry struct {
	ID          string
	Position    int64
	AggregateID string
	EventType   string
	Payload     json.RawMessage
	Topic       string
	// Attempts counts delivery attempts already made, before the current one.
	Attempts int
}

// ClaimOutbox leases up to limit entries ready for delivery. Only the oldest
// pending entry of each aggregate (its head) is eligible, so an aggregate's
// notifications go out one at a time and in order, while a failing aggregate
// does not hold back the others. FOR UPDATE SKIP LOCKED plus the lease keep
// concurrent dispatchers from claiming the same entry; a lease left behind by
// a crashed dispatcher expires and the entry is claimed again.
func (r *Repository) ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]OutboxEntry, error) {
	rows, err := r.pool.Query(ctx, `
		WITH heads AS (
			SELECT o.id FROM outbox o
			WHERE o.processed = FALSE
			  AND o.dead_at IS NULL
			  AND o.next_attempt_at <= NOW()
			  AND (o.lease_until IS NULL OR o.lease_until < NOW())
			  AND NOT EXISTS (
				SELECT 1 FROM outbox prev
				WHERE prev.aggregate_id = o.aggregate_id
				  AND prev.position < o.position
				  AND prev.processed = FALSE
				  AND prev.dead_at IS NULL
			  )
			ORDER BY o.position
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox SET lease_until = NOW() + make_interval(secs => $2)
		FROM heads
		WHERE outbox.id = heads.id
		RETURNING outbox.id::text, outbox.position, outbox.aggregate_id::text,
		          outbox.event_type, outbox.payload, outbox.topic, outbox.attempts
	`, limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[OutboxEntry])
	if err != nil {
		return nil, fmt.Errorf("scan claimed outbox: %w", err)
	}
	return entries, nil
}

// MarkOutboxDelivered records a successful delivery.
func (r *Repository) MarkOutboxDelivered(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox
		SET processed = TRUE, attempts = attempts + 1, lease_until = NULL, last_error = NULL
		WHERE id = $1::uuid
	`, id)
	if err != nil {
		return fmt.Errorf("mark outbox delivered: %w", err)
	}
	return nil
}

// MarkOutboxFailed records a failed attempt. A dead entry is never retried and
// stops blocking the entries after it; otherwise it becomes eligible again
// after retryIn.
func (r *Repository) MarkOutboxFailed(ctx context.Context, id string, retryIn time.Duration, lastErr string, dead bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox
		SET attempts = attempts + 1,
		    lease_until = NULL,
		    last_error = $2,
		    next_attempt_at = NOW() + make_interval(secs => $3),
		    dead_at = CASE WHEN $4 THEN NOW() END
		WHERE id = $1::uuid
	`, id, lastErr, retryIn.Seconds(), dead)
	if err != nil {
		return fmt.Errorf("mark outbox failed: %w", err)
	}
	return nil
}

// ReleaseOutbox gives a claimed entry back without counting an attempt, for
// deliveries that were never tried (circuit breaker open, shutdown).
func (r *Repository) ReleaseOutbox(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE outbox SET lease_until = NULL WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("release outbox: %w", err)
	}
	return nil
}

// OutboxBacklog counts notifications not yet delivered: pending (waiting for
// delivery or a retry) and dead-lettered.
func (r *Repository) OutboxBacklog(ctx context.Context) (pending, dead int64, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE dead_at IS NULL),
		       COUNT(*) FILTER (WHERE dead_at IS NOT NULL)
		FROM outbox
		WHERE processed = FALSE
	`).Scan(&pending, &dead)
	if err != nil {
		return 0, 0, fmt.Errorf("outbox backlog: %w", err)
	}
	return pending, dead, nil
}
