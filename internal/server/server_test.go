package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ratelimiter/internal/limiter"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	h   http.Handler
	clk *testClock
}

// newHarness builds a server with a 1 token/sec, burst-3 limiter on a fake clock.
func newHarness(t *testing.T, opts ...limiter.Option) *harness {
	t.Helper()
	clk := &testClock{t: time.Unix(1_700_000_000, 0)}
	opts = append([]limiter.Option{limiter.WithClock(clk.Now)}, opts...)
	lim, err := limiter.New(1, 3, opts...)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &harness{h: New(lim, "test-1.0", log).Handler(), clk: clk}
}

func (h *harness) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("response is not JSON: %q (%v)", rec.Body.String(), err)
	}
}

func TestCheckAllowThenDeny(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < 3; i++ {
		rec := h.do("POST", "/check", `{"key":"user42"}`)
		if rec.Code != 200 {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
		var res checkResponse
		decode(t, rec, &res)
		if !res.Allowed || res.Remaining != 2-i || res.Limit != 3 {
			t.Fatalf("request %d: %+v", i, res)
		}
		if got := rec.Header().Get("X-RateLimit-Remaining"); got != itoa(2-i) {
			t.Fatalf("X-RateLimit-Remaining = %q", got)
		}
	}

	rec := h.do("POST", "/check", `{"key":"user42"}`)
	if rec.Code != 429 {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	var res checkResponse
	decode(t, rec, &res)
	if res.Allowed || res.RetryAfterSeconds != 1 {
		t.Fatalf("denied body: %+v", res)
	}

	h.clk.Advance(time.Second)
	if rec := h.do("POST", "/check", `{"key":"user42"}`); rec.Code != 200 {
		t.Fatalf("after waiting Retry-After: status %d, want 200", rec.Code)
	}
}

func TestCheckCost(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("POST", "/check", `{"key":"k","cost":3}`); rec.Code != 200 {
		t.Fatalf("cost 3 of 3: status %d", rec.Code)
	}
	rec := h.do("POST", "/check", `{"key":"k","cost":2}`)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status %d Retry-After %q, want 429 and 2", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestCheckRejectsBadInput(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("a", maxKeyLen+1)
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"empty body":       {``, 400},
		"not json":         {`hello`, 400},
		"missing key":      {`{}`, 400},
		"empty key":        {`{"key":""}`, 400},
		"key too long":     {`{"key":"` + long + `"}`, 400},
		"unknown field":    {`{"key":"k","bogus":1}`, 400},
		"wrong key type":   {`{"key":42}`, 400},
		"cost zero":        {`{"key":"k","cost":0}`, 400},
		"cost negative":    {`{"key":"k","cost":-1}`, 400},
		"cost above burst": {`{"key":"k","cost":4}`, 400},
		"two documents":    {`{"key":"k"}{"key":"k"}`, 400},
		"body too large":   {`{"key":"k","pad":"` + strings.Repeat("x", 2000) + `"}`, 413},
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.do("POST", "/check", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			var e map[string]string
			decode(t, rec, &e)
			if e["error"] == "" {
				t.Fatalf("no error message in %q", rec.Body.String())
			}
		})
	}
}

func TestCapacityExhaustedIs503(t *testing.T) {
	h := newHarness(t, limiter.WithMaxKeys(1))
	if rec := h.do("POST", "/check", `{"key":"a"}`); rec.Code != 200 {
		t.Fatalf("first key: %d", rec.Code)
	}
	rec := h.do("POST", "/check", `{"key":"b"}`)
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d Retry-After %q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestHealthAndVersion(t *testing.T) {
	h := newHarness(t)

	rec := h.do("GET", "/health", "")
	var health map[string]string
	decode(t, rec, &health)
	if rec.Code != 200 || health["status"] != "ok" {
		t.Fatalf("/health: %d %v", rec.Code, health)
	}

	rec = h.do("GET", "/version", "")
	var ver map[string]string
	decode(t, rec, &ver)
	if rec.Code != 200 || ver["version"] != "test-1.0" {
		t.Fatalf("/version: %d %v", rec.Code, ver)
	}
}

func TestConfigEndpoint(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/config", "")
	var cfg struct {
		Rate  float64 `json:"rate_per_sec"`
		Burst int     `json:"burst"`
	}
	decode(t, rec, &cfg)
	if rec.Code != 200 || cfg.Rate != 1 || cfg.Burst != 3 {
		t.Fatalf("/config: %d %+v, want 200 rate=1 burst=3", rec.Code, cfg)
	}
}

func TestUI(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET /: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<title>ratelimiter</title>") {
		t.Error("GET / did not return the UI page")
	}
	rec = h.do("GET", "/assets/inter.woff2", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" ||
		!strings.HasPrefix(rec.Body.String(), "wOF2") {
		t.Errorf("GET /assets/inter.woff2: %d %q (%d bytes)", rec.Code,
			rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	// "/" must not act as a catch-all: other unknown paths stay 404.
	if rec := h.do("GET", "/index.html", ""); rec.Code != 404 {
		t.Errorf("GET /index.html: %d, want 404", rec.Code)
	}
	if rec := h.do("GET", "/favicon.ico", ""); rec.Code != 204 {
		t.Errorf("GET /favicon.ico: %d, want 204", rec.Code)
	}
}

func TestRoutingErrors(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/nope", ""); rec.Code != 404 {
		t.Errorf("unknown path: %d, want 404", rec.Code)
	}
	rec := h.do("GET", "/check", "")
	if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "POST") {
		t.Errorf("GET /check: %d Allow=%q, want 405 allowing POST", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := h.do("POST", "/health", ""); rec.Code != 405 {
		t.Errorf("POST /health: %d, want 405", rec.Code)
	}
}

func TestMetrics(t *testing.T) {
	h := newHarness(t)

	// Series exist before any traffic.
	pre := h.do("GET", "/metrics", "").Body.String()
	for _, want := range []string{
		`ratelimiter_checks_total{result="denied"} 0`,
		`ratelimiter_build_info{version="test-1.0"} 1`,
		`ratelimiter_active_keys 0`,
	} {
		if !strings.Contains(pre, want) {
			t.Errorf("fresh /metrics missing %q", want)
		}
	}

	for i := 0; i < 4; i++ { // 3 allowed + 1 denied
		h.do("POST", "/check", `{"key":"user42"}`)
	}
	h.do("POST", "/check", `{}`) // 400
	h.do("GET", "/whatever", "") // 404

	out := h.do("GET", "/metrics", "").Body.String()
	for _, want := range []string{
		`ratelimiter_checks_total{result="allowed"} 3`,
		`ratelimiter_checks_total{result="denied"} 1`,
		`ratelimiter_http_requests_total{code="200",method="POST",path="/check"} 3`,
		`ratelimiter_http_requests_total{code="429",method="POST",path="/check"} 1`,
		`ratelimiter_http_requests_total{code="400",method="POST",path="/check"} 1`,
		`ratelimiter_http_requests_total{code="404",method="GET",path="unmatched"} 1`,
		`ratelimiter_http_request_duration_seconds_count{path="/check"} 5`,
		`ratelimiter_active_keys 1`,
		`process_start_time_seconds`,
		`go_goroutines`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}

func TestMetricMethodLabelIsBounded(t *testing.T) {
	h := newHarness(t)
	h.do("FROBNICATE", "/check", "")
	if out := h.do("GET", "/metrics", "").Body.String(); !strings.Contains(out, `method="OTHER"`) {
		t.Error("unknown HTTP methods should be reported as OTHER")
	}
}

func TestPanicBecomes500(t *testing.T) {
	lim, _ := limiter.New(1, 1)
	s := New(lim, "v", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := s.instrument(s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 500 {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
