package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var loops = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "selfmail_loop_timestamp_seconds", Help: "Last component loop, including idle work"}, []string{"component"})
var successes = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "selfmail_success_timestamp_seconds", Help: "Last successful component operation, including empty polls"}, []string{"component"})
var failures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "selfmail_operation_errors_total", Help: "Component operation failures"}, []string{"component"})
var consumers = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "selfmail_broker_consumers", Help: "Connected consumers by priority"}, []string{"priority"})
var outcomes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "selfmail_handoff_total", Help: "Local SMTP handoff outcomes"}, []string{"outcome"})
var work = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "selfmail_work_seconds", Help: "Component work and journal lock timing", Buckets: []float64{.0001, .0005, .001, .002, .005, .01, .02, .05, .1, .25, .5, 1, 5, 15}}, []string{"stage"})

func ObserveWork(stage string, elapsed time.Duration) {
	work.WithLabelValues(stage).Observe(elapsed.Seconds())
}

func Progress(component string, e error) {
	loops.WithLabelValues(component).Set(float64(time.Now().Unix()))
	if e == nil {
		successes.WithLabelValues(component).Set(float64(time.Now().Unix()))
	} else {
		failures.WithLabelValues(component).Inc()
	}
}
func Consumer(priority string, delta float64) { consumers.WithLabelValues(priority).Add(delta) }
func Handoff(outcome string)                  { outcomes.WithLabelValues(outcome).Inc() }
