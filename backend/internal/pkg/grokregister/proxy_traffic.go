package grokregister

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// Proxy traffic accounting for residential/SOCKS paths used by Grok register.
// Counts request body + response body bytes when a proxy is active.
// Approximate (excludes TLS framing / SOCKS overhead) but good enough for ops dashboards.

var (
	// Task-scoped counters (reset at RunBatch start).
	taskProxyTx   atomic.Int64
	taskProxyRx   atomic.Int64
	taskProxyReqs atomic.Int64

	// Process-lifetime totals (not persisted; persisted aggregates live in service layer).
	procProxyTx   atomic.Int64
	procProxyRx   atomic.Int64
	procProxyReqs atomic.Int64

	// Optional sink: service registers to persist daily buckets without importing service.
	trafficSinkMu sync.RWMutex
	trafficSink   func(tx, rx, reqs int64)
)

// ResetTaskProxyTraffic clears per-batch counters (call at RunBatch start).
func ResetTaskProxyTraffic() {
	taskProxyTx.Store(0)
	taskProxyRx.Store(0)
	taskProxyReqs.Store(0)
}

// TaskProxyTraffic returns current batch tx/rx/request counts.
func TaskProxyTraffic() (tx, rx, reqs int64) {
	return taskProxyTx.Load(), taskProxyRx.Load(), taskProxyReqs.Load()
}

// ProcessProxyTraffic returns process-lifetime counters (since boot).
func ProcessProxyTraffic() (tx, rx, reqs int64) {
	return procProxyTx.Load(), procProxyRx.Load(), procProxyReqs.Load()
}

// SetProxyTrafficSink registers a callback invoked on each AddProxyTraffic
// (used by service to roll daily aggregates). Pass nil to clear.
func SetProxyTrafficSink(fn func(tx, rx, reqs int64)) {
	trafficSinkMu.Lock()
	trafficSink = fn
	trafficSinkMu.Unlock()
}

// AddProxyTraffic records bytes that went through a proxy URL.
// tx = request body (+ rough header allowance applied by caller if desired),
// rx = response body length. No-op when both zero.
func AddProxyTraffic(tx, rx int64) {
	if tx < 0 {
		tx = 0
	}
	if rx < 0 {
		rx = 0
	}
	if tx == 0 && rx == 0 {
		return
	}
	taskProxyTx.Add(tx)
	taskProxyRx.Add(rx)
	taskProxyReqs.Add(1)
	procProxyTx.Add(tx)
	procProxyRx.Add(rx)
	procProxyReqs.Add(1)

	trafficSinkMu.RLock()
	fn := trafficSink
	trafficSinkMu.RUnlock()
	if fn != nil {
		fn(tx, rx, 1)
	}
}

// countingRoundTripper wraps an http.RoundTripper and records body sizes when
// the client is known to be proxy-backed (caller only installs this on proxy clients).
type countingRoundTripper struct {
	base http.RoundTripper
}

func (c countingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := c.base
	if base == nil {
		base = http.DefaultTransport
	}
	var tx int64
	if req != nil && req.Body != nil && req.ContentLength > 0 {
		tx = req.ContentLength
	} else if req != nil && req.Body != nil {
		// Unknown length: leave tx=0 (avoid draining body).
		tx = 0
	}
	// Rough HTTP header overhead for outbound.
	if req != nil {
		tx += 200
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		if tx > 0 {
			AddProxyTraffic(tx, 0)
		}
		return resp, err
	}
	var rxHint int64
	if resp.ContentLength > 0 {
		rxHint = resp.ContentLength
	}
	if resp.Body != nil {
		// Count once on Close from actual bytes read (or Content-Length hint).
		resp.Body = &countingReadCloser{rc: resp.Body, tx: tx, rxHint: rxHint}
	} else {
		AddProxyTraffic(tx, rxHint)
	}
	return resp, nil
}

type countingReadCloser struct {
	rc      io.ReadCloser
	tx      int64
	rxHint  int64
	read    int64
	counted bool
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.read += int64(n)
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	if !c.counted {
		rx := c.read
		if rx == 0 && c.rxHint > 0 {
			rx = c.rxHint
		}
		AddProxyTraffic(c.tx, rx)
		c.counted = true
	}
	return c.rc.Close()
}
