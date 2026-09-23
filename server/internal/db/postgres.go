package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/user/nexus-server/internal/sequencing"
	"github.com/user/nexus-server/pkg/models"
)

const notificationsTopic = "order-notifications"

type Repository struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Repository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}
	slog.Info("connected to Postgres")
	return &Repository{pool: pool}, nil
}

func (r *Repository) Close() {
	r.pool.Close()
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// Result describes what ApplyEvent or AbortPlan did to the order.
type Result struct {
	Outcome sequencing.Outcome
	// LastSeq is the order's last applied seq_id once the transaction commits,
	// including buffered events drained by it.
	LastSeq int
	// Drained counts buffered events applied in the same transaction.
	Drained int
}

// IsPermanent reports whether retrying err can never succeed because the
// database rejected the data itself (SQLSTATE class 22 data exception or 23
// integrity violation). Connection loss, timeouts and serialization failures
// are transient.
func IsPermanent(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")
	}
	return false
}

// ApplyEvent decides and persists a sequenced event in a single transaction:
// consecutive events update the order and the outbox, then drain any buffered
// successors; out-of-order events are buffered. Postgres is the only source of
// truth for sequencing, so a failed transaction leaves no trace anywhere.
func (r *Repository) ApplyEvent(ctx context.Context, event *models.EventEnvelope) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		state, err := lockOrder(ctx, tx, event.OrderID)
		if err != nil {
			return err
		}
		res = Result{Outcome: sequencing.Decide(state, event.PlanID, *event.SeqID), LastSeq: state.LastSeq}

		switch res.Outcome {
		case sequencing.Apply:
			if err := applyEvent(ctx, tx, state.Exists, event); err != nil {
				return err
			}
			drained, err := drainPending(ctx, tx, event.OrderID, event.PlanID, *event.SeqID+1)
			if err != nil {
				return err
			}
			res.Drained = drained
			res.LastSeq = *event.SeqID + drained
		case sequencing.Buffer:
			return bufferEvent(ctx, tx, event)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// AbortPlan aborts the order of event's plan and discards its buffered events.
// Downstream is notified only when a started order is aborted; an abort that
// arrives before any event leaves a tombstone so late events are discarded.
func (r *Repository) AbortPlan(ctx context.Context, event *models.EventEnvelope) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		state, err := lockOrder(ctx, tx, event.OrderID)
		if err != nil {
			return err
		}
		res = Result{Outcome: sequencing.DecideAbort(state, event.PlanID), LastSeq: state.LastSeq}

		switch res.Outcome {
		case sequencing.Apply:
			if _, err := tx.Exec(ctx, `
				UPDATE orders SET status = $2, updated_at = NOW() WHERE id = $1::uuid
			`, event.OrderID, models.StatusAborted); err != nil {
				return fmt.Errorf("abort order: %w", err)
			}
			if err := insertOutbox(ctx, tx, event); err != nil {
				return err
			}
		case sequencing.Tombstone:
			if _, err := tx.Exec(ctx, `
				INSERT INTO orders (id, user_id, status, plan_id, last_seq_processed)
				VALUES ($1::uuid, $2, $3, $4, 0)
			`, event.OrderID, userID(event), models.StatusAborted, event.PlanID); err != nil {
				return fmt.Errorf("insert abort tombstone: %w", err)
			}
		default:
			return nil
		}

		if _, err := tx.Exec(ctx, `
			DELETE FROM pending_events WHERE order_id = $1::uuid AND plan_id = $2
		`, event.OrderID, event.PlanID); err != nil {
			return fmt.Errorf("discard pending events: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// ExpirePending hands buffered events older than maxAge to sink, oldest first,
// and deletes them in the same transaction: an event leaves the buffer only
// after sink accepted it. Returns how many events were expired.
func (r *Repository) ExpirePending(ctx context.Context, maxAge time.Duration, limit int, sink func(context.Context, *models.EventEnvelope) error) (int, error) {
	expired := 0
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT order_id::text, plan_id, seq_id, payload FROM pending_events
			WHERE received_at < NOW() - make_interval(secs => $1)
			ORDER BY received_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		`, maxAge.Seconds(), limit)
		if err != nil {
			return fmt.Errorf("select expired pending events: %w", err)
		}
		pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pendingRow])
		if err != nil {
			return fmt.Errorf("scan expired pending events: %w", err)
		}

		for _, p := range pending {
			var event models.EventEnvelope
			if err := json.Unmarshal(p.Payload, &event); err != nil {
				return fmt.Errorf("decode pending event: %w", err)
			}
			if err := sink(ctx, &event); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				DELETE FROM pending_events WHERE order_id = $1::uuid AND plan_id = $2 AND seq_id = $3
			`, p.OrderID, p.PlanID, p.SeqID); err != nil {
				return fmt.Errorf("delete expired pending event: %w", err)
			}
			expired++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expired, nil
}

type pendingRow struct {
	OrderID string
	PlanID  string
	SeqID   int
	Payload []byte
}

// lockOrder serializes every writer of orderID for the rest of the transaction
// and returns the order's current state. An advisory lock is used instead of
// SELECT ... FOR UPDATE because the order row may not exist yet.
func lockOrder(ctx context.Context, tx pgx.Tx, orderID string) (sequencing.OrderState, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, orderID); err != nil {
		return sequencing.OrderState{}, fmt.Errorf("lock order: %w", err)
	}

	var s sequencing.OrderState
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(plan_id, ''), COALESCE(last_seq_processed, 0), status
		FROM orders WHERE id = $1::uuid
	`, orderID).Scan(&s.PlanID, &s.LastSeq, &s.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read order state: %w", err)
	}
	s.Exists = true
	return s, nil
}

func applyEvent(ctx context.Context, tx pgx.Tx, orderExists bool, event *models.EventEnvelope) error {
	status := models.StatusFromEventType(event.EventType)
	var totalAmount *float64
	if am, ok := event.Data["total_amount"].(float64); ok {
		totalAmount = &am
	}

	var err error
	if orderExists {
		_, err = tx.Exec(ctx, `
			UPDATE orders
			SET status = $2,
			    total_amount = COALESCE($3, total_amount),
			    last_seq_processed = $4,
			    updated_at = NOW()
			WHERE id = $1::uuid
		`, event.OrderID, status, totalAmount, *event.SeqID)
	} else {
		_, err = tx.Exec(ctx, `
			INSERT INTO orders (id, user_id, status, plan_id, total_amount, last_seq_processed)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)
		`, event.OrderID, userID(event), status, event.PlanID, totalAmount, *event.SeqID)
	}
	if err != nil {
		return fmt.Errorf("write order: %w", err)
	}
	return insertOutbox(ctx, tx, event)
}

// drainPending applies buffered events of planID starting at next, for as long
// as they are consecutive. Returns how many were applied.
func drainPending(ctx context.Context, tx pgx.Tx, orderID, planID string, next int) (int, error) {
	drained := 0
	for ; ; next++ {
		var payload []byte
		err := tx.QueryRow(ctx, `
			DELETE FROM pending_events
			WHERE order_id = $1::uuid AND plan_id = $2 AND seq_id = $3
			RETURNING payload
		`, orderID, planID, next).Scan(&payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return drained, nil
		}
		if err != nil {
			return drained, fmt.Errorf("take pending event: %w", err)
		}

		var event models.EventEnvelope
		if err := json.Unmarshal(payload, &event); err != nil {
			return drained, fmt.Errorf("decode pending event: %w", err)
		}
		if err := applyEvent(ctx, tx, true, &event); err != nil {
			return drained, err
		}
		drained++
	}
}

func bufferEvent(ctx context.Context, tx pgx.Tx, event *models.EventEnvelope) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal pending event: %w", err)
	}
	// A redelivered out-of-order event is already buffered: nothing to do.
	_, err = tx.Exec(ctx, `
		INSERT INTO pending_events (order_id, plan_id, seq_id, payload)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT DO NOTHING
	`, event.OrderID, event.PlanID, *event.SeqID, payload)
	if err != nil {
		return fmt.Errorf("buffer event: %w", err)
	}
	return nil
}

func insertOutbox(ctx context.Context, tx pgx.Tx, event *models.EventEnvelope) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event for outbox: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, event_type, payload, topic)
		VALUES ($1::uuid, $2, $3, $4)
	`, event.OrderID, event.EventType, payload, notificationsTopic)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}
	return nil
}

func userID(event *models.EventEnvelope) string {
	if u, ok := event.Data["user_id"].(string); ok {
		return u
	}
	return "unknown"
}

// FetchUnprocessedOutbox returns up to `limit` unprocessed outbox entries.
func (r *Repository) FetchUnprocessedOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, aggregate_id, event_type, payload, topic
		FROM outbox
		WHERE processed = FALSE
		ORDER BY position
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch outbox: %w", err)
	}
	defer rows.Close()

	var entries []OutboxEntry
	for rows.Next() {
		var e OutboxEntry
		if err := rows.Scan(&e.ID, &e.AggregateID, &e.EventType, &e.Payload, &e.Topic); err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// MarkOutboxProcessed marks an outbox entry as processed.
func (r *Repository) MarkOutboxProcessed(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `UPDATE outbox SET processed = TRUE WHERE id = $1::uuid`, id)
	return err
}

type OutboxEntry struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     json.RawMessage
	Topic       string
}
