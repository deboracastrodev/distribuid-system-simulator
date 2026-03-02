package redis

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	rdb        *redis.Client
	luaScript  *redis.Script
	bufferTTL  time.Duration
}

func New(addr, password string, bufferTTL time.Duration, luaScriptPath string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
	})

	luaBytes, err := os.ReadFile(luaScriptPath)
	if err != nil {
		return nil, fmt.Errorf("reading lua script %s: %w", luaScriptPath, err)
	}

	return &Client{
		rdb:       rdb,
		luaScript: redis.NewScript(string(luaBytes)),
		bufferTTL: bufferTTL,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

// CheckAndSetSeq runs the Lua script for atomic sequence validation.
// Returns: "OK", "DUPLICATE", "OUT_OF_ORDER", or "ABORTED".
func (c *Client) CheckAndSetSeq(ctx context.Context, planID string, seqID int) (string, error) {
	keys := []string{
		fmt.Sprintf("seq:%s", planID),
		fmt.Sprintf("aborted:%s", planID),
	}
	result, err := c.luaScript.Run(ctx, c.rdb, keys, seqID).Text()
	if err != nil {
		return "", fmt.Errorf("lua check_and_set_seq: %w", err)
	}
	return result, nil
}

// BufferEvent stores an out-of-order event in a sorted set keyed by plan_id.
func (c *Client) BufferEvent(ctx context.Context, planID string, seqID int, payload []byte) error {
	key := fmt.Sprintf("buffer:%s", planID)
	member := redis.Z{
		Score:  float64(seqID),
		Member: string(payload),
	}
	if err := c.rdb.ZAdd(ctx, key, member).Err(); err != nil {
		return fmt.Errorf("buffering event: %w", err)
	}
	c.rdb.Expire(ctx, key, c.bufferTTL)
	slog.Info("event buffered", "plan_id", planID, "seq_id", seqID)
	return nil
}

// DrainBuffer retrieves and removes consecutive events from the buffer starting at nextSeq.
// Returns events in order, each as raw JSON bytes.
func (c *Client) DrainBuffer(ctx context.Context, planID string, nextSeq int) ([][]byte, error) {
	key := fmt.Sprintf("buffer:%s", planID)
	var drained [][]byte

	for {
		// Fetch the lowest-scored member
		results, err := c.rdb.ZRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{
			Min:   strconv.Itoa(nextSeq),
			Max:   strconv.Itoa(nextSeq),
			Count: 1,
		}).Result()
		if err != nil || len(results) == 0 {
			break
		}

		payload := results[0].Member.(string)
		c.rdb.ZRem(ctx, key, payload)
		drained = append(drained, []byte(payload))
		nextSeq++
	}

	return drained, nil
}

// AbortPlan sets the abort flag and clears the buffer for a plan.
func (c *Client) AbortPlan(ctx context.Context, planID string) error {
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, fmt.Sprintf("aborted:%s", planID), "1", 24*time.Hour)
	pipe.Del(ctx, fmt.Sprintf("buffer:%s", planID))
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("aborting plan %s: %w", planID, err)
	}
	slog.Info("plan aborted in Redis", "plan_id", planID)
	return nil
}

// TTL returns the remaining time to live of a key.
func (c *Client) TTL(ctx context.Context, key string) (time.Duration, error) {
	return c.rdb.TTL(ctx, key).Result()
}

// GetExpiredBufferKeys scans for buffer keys.
func (c *Client) GetBufferKeys(ctx context.Context) ([]string, error) {
	var keys []string
	iter := c.rdb.Scan(ctx, 0, "buffer:*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	return keys, iter.Err()
}

// GetBufferMembers returns all members of a buffer sorted set.
func (c *Client) GetBufferMembers(ctx context.Context, key string) ([]string, error) {
	return c.rdb.ZRange(ctx, key, 0, -1).Result()
}

// DeleteKey removes a key from Redis.
func (c *Client) DeleteKey(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}
