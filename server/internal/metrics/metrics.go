// Package metrics defines the server's Prometheus metrics. Components receive
// a *Metrics instead of using globals, so each test can count on a fresh
// registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "nexus"

// Label values known up front. Pre-initializing them makes every series exist
// with value 0, so dashboards and alerts see "0" instead of "no data".
var (
	eventKinds     = []string{"sequenced", "abort"}
	eventOutcomes  = []string{"apply", "duplicate", "buffer", "discard", "plan_mismatch", "tombstone"}
	dlqCodes       = []string{"PARSE_ERROR", "INVALID_EVENT", "DB_REJECTED", "PLAN_MISMATCH", "BUFFER_TIMEOUT"}
	webhookResults = []string{"delivered", "retry_scheduled", "dead_max_attempts", "dead_rejected", "released"}
	cbStates       = []string{"closed", "half-open", "open"}
)

// Values of CircuitBreakerState.
const (
	CBClosed   = 0
	CBHalfOpen = 1
	CBOpen     = 2
)

type Metrics struct {
	// Consumer
	EventsProcessed    *prometheus.CounterVec
	EventsDrained      prometheus.Counter
	DLQMessages        *prometheus.CounterVec
	ConsumerRetries    prometheus.Counter
	EventSettleSeconds prometheus.Histogram
	ConsumerLag        *prometheus.GaugeVec

	// Outbox dispatcher
	WebhookDeliveries         *prometheus.CounterVec
	WebhookRequestSeconds     prometheus.Histogram
	CircuitBreakerState       prometheus.Gauge
	CircuitBreakerTransitions *prometheus.CounterVec
}

func New(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	m := &Metrics{
		EventsProcessed: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "events_processed_total",
			Help: "Events settled by the consumer, by kind and outcome of the sequencing decision.",
		}, []string{"kind", "outcome"}),
		EventsDrained: f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "events_drained_total",
			Help: "Buffered out-of-order events applied when their predecessor arrived.",
		}),
		DLQMessages: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "dlq_messages_total",
			Help: "Messages acknowledged by the dead letter queue, by error code.",
		}, []string{"code"}),
		ConsumerRetries: f.NewCounter(prometheus.CounterOpts{
			Namespace: namespace, Name: "consumer_retries_total",
			Help: "Transient failures retried in place by the consumer.",
		}),
		EventSettleSeconds: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "event_settle_seconds",
			Help:    "Time from the first processing attempt of a record until it is settled, retries included.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}),
		ConsumerLag: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "consumer_lag",
			Help: "Records behind the partition high watermark, measured after each processed batch.",
		}, []string{"topic", "partition"}),
		WebhookDeliveries: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "webhook_deliveries_total",
			Help: "Outcome of each outbox webhook delivery attempt.",
		}, []string{"result"}),
		WebhookRequestSeconds: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "webhook_request_seconds",
			Help:    "Duration of webhook HTTP requests, successful or not.",
			Buckets: prometheus.DefBuckets,
		}),
		CircuitBreakerState: f.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "circuit_breaker_state",
			Help: "Webhook circuit breaker state: 0 closed, 1 half-open, 2 open.",
		}),
		CircuitBreakerTransitions: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "circuit_breaker_transitions_total",
			Help: "Webhook circuit breaker state changes, by the state entered.",
		}, []string{"to"}),
	}

	for _, kind := range eventKinds {
		for _, outcome := range eventOutcomes {
			m.EventsProcessed.WithLabelValues(kind, outcome)
		}
	}
	for _, code := range dlqCodes {
		m.DLQMessages.WithLabelValues(code)
	}
	for _, result := range webhookResults {
		m.WebhookDeliveries.WithLabelValues(result)
	}
	for _, state := range cbStates {
		m.CircuitBreakerTransitions.WithLabelValues(state)
	}
	return m
}

// NewForTest returns metrics on a registry of their own.
func NewForTest() *Metrics {
	return New(prometheus.NewRegistry())
}
