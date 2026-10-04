// Package server exposes the limiter over HTTP.
package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ratelimiter/internal/limiter"
)

const (
	maxBodyBytes = 1 << 10 // a check request is tiny; refuse anything bigger
	maxKeyLen    = 256     // bounds per-key memory
)

//go:embed web/index.html
var indexHTML []byte

// Inter (SIL Open Font License, see web/OFL-Inter.txt), embedded so the UI
// looks the same offline and inside containers.
//
//go:embed web/inter.woff2
var interFont []byte

// Server wires the limiter to HTTP handlers, metrics and logging.
type Server struct {
	lim     *limiter.Limiter
	version string
	log     *slog.Logger
	m       *metrics
}

func New(lim *limiter.Limiter, version string, log *slog.Logger) *Server {
	return &Server{lim: lim, version: version, log: log, m: newMetrics(lim, version)}
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleUI)
	mux.HandleFunc("GET /assets/inter.woff2", s.handleFont)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent) // keeps browsers from logging 404s
	})
	mux.HandleFunc("POST /check", s.handleCheck)
	mux.HandleFunc("GET /config", s.handleConfig)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.m.reg, promhttp.HandlerOpts{}))
	return s.instrument(s.recoverPanics(mux))
}

type checkRequest struct {
	Key  string `json:"key"`
	Cost *int   `json:"cost"` // optional, defaults to 1
}

type checkResponse struct {
	Allowed           bool    `json:"allowed"`
	Remaining         int     `json:"remaining"`
	Limit             int     `json:"limit"`
	RetryAfterSeconds float64 `json:"retry_after_seconds"`
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req checkRequest
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "body must be JSON like {\"key\":\"user42\"}")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "body must contain a single JSON object")
		return
	}
	if req.Key == "" || len(req.Key) > maxKeyLen {
		writeError(w, http.StatusBadRequest, "key must be 1 to "+strconv.Itoa(maxKeyLen)+" bytes")
		return
	}
	cost := 1
	if req.Cost != nil {
		cost = *req.Cost
	}
	if cost < 1 || cost > s.lim.Burst() {
		writeError(w, http.StatusBadRequest, "cost must be between 1 and "+strconv.Itoa(s.lim.Burst()))
		return
	}

	res, err := s.lim.Allow(req.Key, cost)
	if err != nil {
		// Only ErrCapacity is possible here: cost was validated above.
		s.m.checks.WithLabelValues(resultError).Inc()
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "too many tracked keys, try again shortly")
		return
	}

	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(s.lim.Burst()))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
	body := checkResponse{
		Allowed:           res.Allowed,
		Remaining:         res.Remaining,
		Limit:             s.lim.Burst(),
		RetryAfterSeconds: math.Round(res.RetryAfter.Seconds()*1000) / 1000,
	}
	if res.Allowed {
		s.m.checks.WithLabelValues(resultAllowed).Inc()
		writeJSON(w, http.StatusOK, body)
		return
	}
	s.m.checks.WithLabelValues(resultDenied).Inc()
	secs := int(math.Ceil(res.RetryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	h.Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, body)
}

func (s *Server) handleUI(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleFont(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "font/woff2")
	h.Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(interFont)
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"rate_per_sec": s.lim.Rate(),
		"burst":        s.lim.Burst(),
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusRecorder remembers the status code so middleware can report it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// routeLabel turns a ServeMux pattern ("POST /check") into a bounded metric
// label ("/check"). Requests that matched nothing share one label.
func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	if pattern == "/{$}" { // ServeMux syntax for "exactly /"
		return "/"
	}
	return pattern
}

// instrument records metrics and an access-log line for every request.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		route := routeLabel(r.Pattern) // set by ServeMux during next.ServeHTTP
		s.m.requests.WithLabelValues(methodLabel(r.Method), route, strconv.Itoa(rec.status)).Inc()
		s.m.duration.WithLabelValues(route).Observe(elapsed.Seconds())

		// Probes and scrapes fire constantly; keep them out of the info log.
		level := slog.LevelInfo
		if route == "/health" || route == "/metrics" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", route,
			"status", rec.status,
			"duration_ms", float64(elapsed.Microseconds())/1000,
			"remote", r.RemoteAddr,
		)
	})
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "panic", v)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
