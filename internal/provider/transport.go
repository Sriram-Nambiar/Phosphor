package provider

import (
	"net"
	"net/http"
	"time"
)

// NewOptimizedTransport returns an http.Transport tuned specifically for high-throughput
// LLM gateway traffic: generous persistent connection pools, aggressive keep-alive,
// and zero idle connection starvation.
func NewOptimizedTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 0, // LLMs may have extended TTFT; avoid premature abort
	}
}

// Global shared transport ensuring persistent connection reuse across all providers and requests.
var defaultOptimizedTransport = NewOptimizedTransport()

// NewHTTPClient returns an http.Client utilizing the shared connection-pooled transport.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: defaultOptimizedTransport,
		Timeout:   timeout,
	}
}

// NewStreamingHTTPClient returns an http.Client without an overall client deadline,
// utilizing the shared connection pool while allowing stream duration to be governed by context.
func NewStreamingHTTPClient() *http.Client {
	return &http.Client{
		Transport: defaultOptimizedTransport,
		Timeout:   0,
	}
}
