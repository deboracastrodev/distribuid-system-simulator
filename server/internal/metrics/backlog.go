package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// BacklogSource reports work waiting in the database.
type BacklogSource interface {
	OutboxBacklog(ctx context.Context) (pending, dead int64, err error)
	PendingEventsCount(ctx context.Context) (int64, error)
}

// backlogCollector reads the backlog at scrape time. These are counts of rows
// in a state, not events, so a gauge kept in memory would drift from the
// database across restarts and between instances.
type backlogCollector struct {
	src     BacklogSource
	timeout time.Duration
	outbox  *prometheus.Desc
	pending *prometheus.Desc
}

// RegisterBacklog registers gauges for the outbox and reorder buffer backlog.
func RegisterBacklog(reg prometheus.Registerer, src BacklogSource, timeout time.Duration) error {
	return reg.Register(&backlogCollector{
		src:     src,
		timeout: timeout,
		outbox: prometheus.NewDesc(namespace+"_outbox_entries",
			"Outbox notifications not yet delivered, by state: pending (awaiting delivery or retry) or dead (dead-lettered).",
			[]string{"state"}, nil),
		pending: prometheus.NewDesc(namespace+"_pending_events",
			"Out-of-order events buffered until their predecessor arrives.",
			nil, nil),
	})
}

func (c *backlogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.outbox
	ch <- c.pending
}

// Collect omits a metric whose query fails: a missing series is visible in
// Prometheus, whereas a made-up zero would hide the problem.
func (c *backlogCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	if pending, dead, err := c.src.OutboxBacklog(ctx); err != nil {
		slog.Warn("collecting outbox backlog failed", "error", err)
	} else {
		ch <- prometheus.MustNewConstMetric(c.outbox, prometheus.GaugeValue, float64(pending), "pending")
		ch <- prometheus.MustNewConstMetric(c.outbox, prometheus.GaugeValue, float64(dead), "dead")
	}

	if n, err := c.src.PendingEventsCount(ctx); err != nil {
		slog.Warn("collecting pending events failed", "error", err)
	} else {
		ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, float64(n))
	}
}
