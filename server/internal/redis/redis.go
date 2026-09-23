package redis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// entryTTL bounds how long a finished plan's progress stays cached.
const entryTTL = 24 * time.Hour

// Client is a best-effort cache of each plan's progress in front of Postgres.
// It is written only after Postgres commits, so it may lag behind the database
// but never runs ahead of it: a cache hit is a safe reason to skip an event,
// and losing the cache (restart, eviction, outage) costs only extra DB work.
type Client struct {
	rdb           *redis.Client
	advanceScript *redis.Script
}

func New(addr, password, luaScriptPath string) (*Client, error) {
	luaBytes, err := os.ReadFile(luaScriptPath)
	if err != nil {
		return nil, fmt.Errorf("reading lua script %s: %w", luaScriptPath, err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		// A cache that is slow to fail slows every event down; fail fast and let
		// Postgres decide.
		DialTimeout:  250 * time.Millisecond,
		ReadTimeout:  250 * time.Millisecond,
		WriteTimeout: 250 * time.Millisecond,
		MaxRetries:   1,
	})

	return &Client{
		rdb:           rdb,
		advanceScript: redis.NewScript(string(luaBytes)),
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

// Lookup returns the highest seq_id known to be committed for planID and
// whether the plan is known to be aborted.
func (c *Client) Lookup(ctx context.Context, planID string) (lastSeq int, aborted bool, err error) {
	pipe := c.rdb.Pipeline()
	seqCmd := pipe.Get(ctx, seqKey(planID))
	abortedCmd := pipe.Exists(ctx, abortedKey(planID))
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return 0, false, fmt.Errorf("seq cache lookup: %w", err)
	}

	lastSeq, err = seqCmd.Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, false, fmt.Errorf("seq cache lookup: %w", err)
	}
	return lastSeq, abortedCmd.Val() == 1, nil
}

// Advance records that planID is committed up to seq. The counter only moves
// forward.
func (c *Client) Advance(ctx context.Context, planID string, seq int) error {
	if err := c.advanceScript.Run(ctx, c.rdb, []string{seqKey(planID)}, seq, int(entryTTL.Seconds())).Err(); err != nil {
		return fmt.Errorf("seq cache advance: %w", err)
	}
	return nil
}

// MarkAborted records that planID is aborted.
func (c *Client) MarkAborted(ctx context.Context, planID string) error {
	if err := c.rdb.Set(ctx, abortedKey(planID), "1", entryTTL).Err(); err != nil {
		return fmt.Errorf("seq cache mark aborted: %w", err)
	}
	return nil
}

func seqKey(planID string) string     { return "seq:" + planID }
func abortedKey(planID string) string { return "aborted:" + planID }
