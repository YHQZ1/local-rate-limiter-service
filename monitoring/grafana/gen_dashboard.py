#!/usr/bin/env python3
"""Generates grafana/dashboards/ratelimiter.json.

The JSON is what Grafana loads, but it is long and awkward to edit by hand, so
it is produced from this file. Run from the repository root:

    python3 monitoring/grafana/gen_dashboard.py

then re-apply with `kubectl apply -k monitoring/`.
"""
import json

DS = {"type": "prometheus", "uid": "prometheus"}
GREEN, ORANGE, RED, BLUE, PURPLE, YELLOW = "green", "orange", "red", "blue", "purple", "yellow"

def target(expr, legend="", ref="A"):
    return {"refId": ref, "datasource": DS, "expr": expr, "legendFormat": legend, "editorMode": "code", "range": True}

def stat(pid, title, expr, x, w, unit="none", decimals=None, steps=None, desc=""):
    steps = steps or [{"color": GREEN, "value": None}]
    d = {"unit": unit, "thresholds": {"mode": "absolute", "steps": steps}, "color": {"mode": "thresholds"}}
    if decimals is not None:
        d["decimals"] = decimals
    return {"id": pid, "type": "stat", "title": title, "description": desc, "datasource": DS,
            "gridPos": {"x": x, "y": 0, "w": w, "h": 4},
            "targets": [target(expr)],
            "fieldConfig": {"defaults": d, "overrides": []},
            "options": {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "colorMode": "value", "graphMode": "area", "justifyMode": "auto", "textMode": "value",
                        "orientation": "auto"}}

def ts(pid, title, targets, x, y, w, h=8, unit="none", stack=False, fill=15, overrides=None,
       vmin=None, vmax=None, interp="linear", decimals=None, desc="", color=None, lw=None, softmax=None):
    custom = {"drawStyle": "line", "lineWidth": (0 if stack else 2) if lw is None else lw, "fillOpacity": fill, "showPoints": "never",
              "lineInterpolation": interp, "spanNulls": False,
              "stacking": {"mode": "normal" if stack else "none", "group": "A"}}
    d = {"unit": unit, "custom": custom, "color": color or {"mode": "palette-classic"}}
    if vmin is not None: d["min"] = vmin
    if vmax is not None: d["max"] = vmax
    if decimals is not None: d["decimals"] = decimals
    if softmax is not None: custom["axisSoftMax"] = softmax
    return {"id": pid, "type": "timeseries", "title": title, "description": desc, "datasource": DS,
            "gridPos": {"x": x, "y": y, "w": w, "h": h},
            "targets": targets,
            "fieldConfig": {"defaults": d, "overrides": overrides or []},
            "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                        "tooltip": {"mode": "multi", "sort": "desc"}}}

def color_override(match, color, regex=False):
    return {"matcher": {"id": "byRegexp" if regex else "byName", "options": match},
            "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}

REQ = 'ratelimiter_http_requests_total'
LAT = 'ratelimiter_http_request_duration_seconds_bucket{path="/check"}'
def q(p): return f'histogram_quantile({p}, sum by (le) (rate({LAT}[1m])))'

panels = [
    stat(1, "Pods up", 'sum(up{job="ratelimiter"})', 0, 3, steps=[{"color": RED, "value": None}, {"color": ORANGE, "value": 1}, {"color": GREEN, "value": 2}],
         desc="Pods answering metric scrapes right now."),
    stat(2, "Availability (5m)", f'(1 - (sum(rate({REQ}{{code=~"5.."}}[5m])) or vector(0)) / clamp_min(sum(rate({REQ}[5m])), 0.001)) * 100', 3, 4, unit="percent", decimals=2,
         steps=[{"color": RED, "value": None}, {"color": ORANGE, "value": 99}, {"color": GREEN, "value": 99.9}],
         desc="Share of requests over the last 5 minutes that did not fail with a 5xx. Rate-limited requests (429) count as served."),
    stat(3, "Requests / s", f'sum(rate({REQ}{{path="/check"}}[1m]))', 7, 4, unit="reqps", decimals=1,
         desc="Rate-limit checks received (POST /check)."),
    stat(4, "Error rate (5xx)", f'(sum(rate({REQ}{{code=~"5.."}}[1m])) or vector(0)) / clamp_min(sum(rate({REQ}[1m])), 0.001)', 11, 5, unit="percentunit", decimals=2,
         steps=[{"color": GREEN, "value": None}, {"color": ORANGE, "value": 0.01}, {"color": RED, "value": 0.05}],
         desc="Server errors as a share of all requests. A 429 is the limiter working and is not counted here."),
    stat(5, "p95 latency (/check)", q(0.95), 16, 4, unit="s", decimals=2,
         steps=[{"color": GREEN, "value": None}, {"color": ORANGE, "value": 0.05}, {"color": RED, "value": 0.1}]),
    stat(6, "Oldest pod uptime", 'max(time() - process_start_time_seconds{job="ratelimiter"})', 20, 4, unit="s", decimals=0,
         desc="How long the longest-running pod has been up. Resets for pods replaced by a rollout or restart."),

    ts(7, "Checks per second, by outcome",
       [target('sum by (result) (rate(ratelimiter_checks_total[1m]))', "{{result}}")], 0, 4, 12, unit="reqps", stack=True, fill=70,
       desc="allowed = under the limit, denied = rate limited (429, working as intended), error = could not decide (503).",
       overrides=[color_override("allowed", GREEN), color_override("denied", ORANGE), color_override("error", RED)]),
    ts(8, "Latency percentiles (/check)",
       [target(q(0.5), "p50", "A"), target(q(0.95), "p95", "B"), target(q(0.99), "p99", "C")], 12, 4, 12, unit="s", fill=0,
       overrides=[color_override("p50", GREEN), color_override("p95", ORANGE), color_override("p99", RED)]),

    ts(9, "Error rate over time (5xx)",
       [target(f'(sum(rate({REQ}{{code=~"5.."}}[1m])) or vector(0)) / clamp_min(sum(rate({REQ}[1m])), 0.001)', "5xx share")], 0, 12, 8,
       unit="percentunit", vmin=0, softmax=0.05, fill=25, color={"mode": "fixed", "fixedColor": RED}),
    ts(10, "Requests per second, by status code",
       [target(f'sum by (code) (rate({REQ}[1m]))', "{{code}}")], 8, 12, 8, unit="reqps", stack=True, fill=70,
       overrides=[color_override("2..", GREEN, True), color_override("4..", ORANGE, True), color_override("5..", RED, True)]),
    ts(11, "Active keys per pod",
       [target('ratelimiter_active_keys', "{{pod}}")], 16, 12, 8,
       desc="Each pod keeps its own buckets, so a key's traffic is split across pods."),

    ts(12, "Pods up over time",
       [target('up{job="ratelimiter"}', "{{pod}}")], 0, 20, 12, unit="none", vmin=0, vmax=1.2, interp="stepAfter", fill=10,
       desc="1 = answering scrapes, 0 = failing. A pod briefly drops to 0 while it shuts down during a rollout, then disappears from the chart."),
    ts(13, "Running versions",
       [target('count by (version) (ratelimiter_build_info)', "{{version}}")], 12, 20, 12, stack=True, interp="stepAfter", fill=70,
       desc="Number of pods per release. Watch it change during a rolling update."),
]

dash = {
    "uid": "ratelimiter", "title": "ratelimiter service", "tags": ["ratelimiter"],
    "timezone": "browser", "schemaVersion": 39, "version": 1, "editable": False,
    "graphTooltip": 1, "refresh": "5s", "time": {"from": "now-15m", "to": "now"},
    "templating": {"list": []}, "annotations": {"list": []}, "links": [], "panels": panels,
}
open("monitoring/grafana/dashboards/ratelimiter.json", "w").write(json.dumps(dash, indent=2) + "\n")
print("panels:", len(panels), "bytes:", len(json.dumps(dash, indent=2)))
