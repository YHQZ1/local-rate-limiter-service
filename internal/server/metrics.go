package server

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"ratelimiter/internal/limiter"
)

// Check outcomes, used as the "result" label of ratelimiter_checks_total.
const (
	resultAllowed = "allowed"
	resultDenied  = "denied"
	resultError   = "error"
)

type metrics struct {
	reg      *prometheus.Registry
	checks   *prometheus.CounterVec
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(l *limiter.Limiter, version string) *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimiter_checks_total",
			Help: "Rate-limit decisions, by result (allowed, denied, error).",
		}, []string{"result"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimiter_http_requests_total",
			Help: "HTTP requests served, by method, route and status code.",
		}, []string{"method", "path", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ratelimiter_http_request_duration_seconds",
			Help:    "HTTP request latency, by route.",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"path"}),
	}

	// Make the series exist from the first scrape so rate() and ratio queries
	// work before any traffic has arrived.
	for _, r := range []string{resultAllowed, resultDenied, resultError} {
		m.checks.WithLabelValues(r)
	}

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "ratelimiter_build_info",
		Help:        "Build information; the value is always 1.",
		ConstLabels: prometheus.Labels{"version": version},
	})
	buildInfo.Set(1)

	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.checks, m.requests, m.duration, buildInfo,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "ratelimiter_active_keys",
			Help: "Distinct keys currently tracked.",
		}, func() float64 { return float64(l.Len()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "ratelimiter_evicted_keys_total",
			Help: "Idle keys dropped by the cleanup janitor.",
		}, func() float64 { return float64(l.Evicted()) }),
	)
	return m
}

// knownMethods bounds the "method" label so a client can't create unbounded
// series by sending made-up verbs.
var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true,
	"PATCH": true, "DELETE": true, "OPTIONS": true,
}

func methodLabel(m string) string {
	if knownMethods[m] {
		return m
	}
	return "OTHER"
}
