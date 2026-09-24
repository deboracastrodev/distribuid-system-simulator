package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBacklog struct {
	pending, dead, buffered int64
	outboxErr, pendingErr   error
}

func (f fakeBacklog) OutboxBacklog(context.Context) (int64, int64, error) {
	return f.pending, f.dead, f.outboxErr
}

func (f fakeBacklog) PendingEventsCount(context.Context) (int64, error) {
	return f.buffered, f.pendingErr
}

func TestMetricsFollowPrometheusConventions(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)
	require.NoError(t, RegisterBacklog(reg, fakeBacklog{}, time.Second))

	problems, err := testutil.GatherAndLint(reg)
	require.NoError(t, err)
	assert.Empty(t, problems)
}

func TestLabelledSeriesExistBeforeFirstEvent(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(reg)

	assert.Equal(t, len(eventKinds)*len(eventOutcomes),
		testutil.CollectAndCount(reg, "nexus_events_processed_total"))
	assert.Equal(t, len(dlqCodes), testutil.CollectAndCount(reg, "nexus_dlq_messages_total"))
	assert.Equal(t, len(webhookResults), testutil.CollectAndCount(reg, "nexus_webhook_deliveries_total"))
}

func TestBacklogCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, RegisterBacklog(reg, fakeBacklog{pending: 3, dead: 1, buffered: 7}, time.Second))

	expected := `
# HELP nexus_outbox_entries Outbox notifications not yet delivered, by state: pending (awaiting delivery or retry) or dead (dead-lettered).
# TYPE nexus_outbox_entries gauge
nexus_outbox_entries{state="dead"} 1
nexus_outbox_entries{state="pending"} 3
# HELP nexus_pending_events Out-of-order events buffered until their predecessor arrives.
# TYPE nexus_pending_events gauge
nexus_pending_events 7
`
	assert.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(expected)))
}

func TestBacklogCollectorOmitsFailedQueries(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, RegisterBacklog(reg, fakeBacklog{buffered: 2, outboxErr: errors.New("db down")}, time.Second))

	assert.Equal(t, 0, testutil.CollectAndCount(reg, "nexus_outbox_entries"), "no made-up zero when the query fails")
	assert.Equal(t, 1, testutil.CollectAndCount(reg, "nexus_pending_events"))
}
