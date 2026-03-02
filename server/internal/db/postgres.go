package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/user/nexus-server/pkg/models"
)

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

// ProcessEvent updates the order status and inserts into outbox in a single ACID transaction.
// Returns true if the event was processed, false if it was a duplicate (idempotent).
func (r *Repository) ProcessEvent(ctx context.Context, event *models.EventEnvelope) (bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	newStatus := models.StatusFromEventType(event.EventType)
	if newStatus == "" {
		return false, fmt.Errorf("unknown event type: %s", event.EventType)
	}

	seqID := 0
	if event.SeqID != nil {
		seqID = *event.SeqID
	}

	// Extract optional fields from event data
	userID := "unknown"
	if u, ok := event.Data["user_id"].(string); ok {
		userID = u
	}
	
	var totalAmount *float64
	if am, ok := event.Data["total_amount"].(float64); ok {
		totalAmount = &am
	}

	// Upsert with idempotency check: only update if seq_id > last_seq_processed
	// We use COALESCE for user_id and total_amount to avoid overwriting with defaults if they are missing in subsequent events
	tag, err := tx.Exec(ctx, `
		INSERT INTO orders (id, user_id, status, plan_id, total_amount, last_seq_processed, created_at, updated_at)
		VALUES ($4::uuid, $5, $1, $2, $6, $3, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE
		SET status = EXCLUDED.status,
		    plan_id = EXCLUDED.plan_id,
		    total_amount = COALESCE(EXCLUDED.total_amount, orders.total_amount),
		    last_seq_processed = EXCLUDED.last_seq_processed,
		    updated_at = NOW()
		WHERE orders.last_seq_processed < EXCLUDED.last_seq_processed
	`, newStatus, event.PlanID, seqID, event.OrderID, userID, totalAmount)
	if err != nil {
		return false, fmt.Errorf("upsert order: %w", err)
	}

	if tag.RowsAffected() == 0 {
		slog.Info("duplicate event skipped in DB (idempotency)", "order_id", event.OrderID, "seq_id", seqID)
		return false, nil
	}

	// Insert into outbox
	payload, err := json.Marshal(event)
	if err != nil {
		return false, fmt.Errorf("marshal event for outbox: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, event_type, payload, topic)
		VALUES ($1::uuid, $2, $3, $4)
	`, event.OrderID, event.EventType, payload, "order-notifications")
	if err != nil {
		return false, fmt.Errorf("insert outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tx: %w", err)
	}

	slog.Info("event persisted", "order_id", event.OrderID, "type", event.EventType, "seq_id", seqID)
	return true, nil
}

// AbortOrder marks an order as aborted in a single transaction with outbox entry.
func (r *Repository) AbortOrder(ctx context.Context, event *models.EventEnvelope) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		UPDATE orders SET status = 'aborted', updated_at = NOW()
		WHERE id = $1::uuid AND status != 'aborted'
	`, event.OrderID)
	if err != nil {
		return fmt.Errorf("abort order: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal abort event: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_id, event_type, payload, topic)
		VALUES ($1::uuid, $2, $3, $4)
	`, event.OrderID, "ABORT_PLAN", payload, "order-notifications")
	if err != nil {
		return fmt.Errorf("insert outbox abort: %w", err)
	}

	return tx.Commit(ctx)
}

// FetchUnprocessedOutbox returns up to `limit` unprocessed outbox entries.
func (r *Repository) FetchUnprocessedOutbox(ctx context.Context, limit int) ([]OutboxEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, aggregate_id, event_type, payload, topic
		FROM outbox
		WHERE processed = FALSE
		ORDER BY created_at ASC
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
