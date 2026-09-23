package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := New(mr.Addr(), "", "../../scripts/lua/advance_seq.lua")
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c, mr
}

func TestLookup_UnknownPlan(t *testing.T) {
	c, _ := newTestClient(t)

	lastSeq, aborted, err := c.Lookup(context.Background(), "plan-x")
	require.NoError(t, err)
	assert.Equal(t, 0, lastSeq)
	assert.False(t, aborted)
}

func TestAdvance_OnlyMovesForward(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()

	require.NoError(t, c.Advance(ctx, "p1", 3))
	// A late writer that committed an older seq must not roll the counter back.
	require.NoError(t, c.Advance(ctx, "p1", 2))

	lastSeq, _, err := c.Lookup(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, 3, lastSeq)
	assert.Equal(t, entryTTL, mr.TTL("seq:p1"))
}

func TestMarkAborted(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()

	require.NoError(t, c.MarkAborted(ctx, "p1"))

	_, aborted, err := c.Lookup(ctx, "p1")
	require.NoError(t, err)
	assert.True(t, aborted)
	assert.Equal(t, entryTTL, mr.TTL("aborted:p1"))
}

func TestCacheErrorsWhenRedisIsDown(t *testing.T) {
	c, mr := newTestClient(t)
	mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := c.Lookup(ctx, "p1")
	assert.Error(t, err)
	assert.Error(t, c.Advance(ctx, "p1", 1))
}

func TestNew_FailsWithoutScript(t *testing.T) {
	_, err := New("localhost:0", "", "missing.lua")
	assert.ErrorContains(t, err, "reading lua script")
}
