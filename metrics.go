package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics for Prometheus. They are served on a separate port (METRICS_PORT, 9090),
// which only Prometheus inside the cluster reaches; the Gateway never routes to it.
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route and status code.",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "How long requests take, by route.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"route"})

	cacheRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "memes_cache_requests_total",
		Help: "Cache lookups for meme lists, by result (hit or miss).",
	}, []string{"result"})
)

// observe records one finished request. The route label is the mux pattern
// ("GET /api/memes/{id}"), not the raw path, so the number of label values stays small.
func observe(r *http.Request, status int, took time.Duration) {
	route := r.Pattern
	if route == "" {
		route = "unmatched"
	}
	httpRequests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
	httpDuration.WithLabelValues(route).Observe(took.Seconds())
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	return mux
}
