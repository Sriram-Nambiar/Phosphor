package router

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

// HealthChecker periodically probes upstream providers to track reachability and accelerate breaker recovery.
type HealthChecker struct {
	router       *Router
	interval     time.Duration
	client       *http.Client
	stopCh       chan struct{}
	wg           sync.WaitGroup
	statusMu     sync.RWMutex
	liveStatuses map[string]bool
	started      bool
}

// NewHealthChecker creates a new background health checker for the given router.
func NewHealthChecker(r *Router, interval time.Duration) *HealthChecker {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &HealthChecker{
		router:       r,
		interval:     interval,
		client:       &http.Client{Timeout: 5 * time.Second},
		stopCh:       make(chan struct{}),
		liveStatuses: make(map[string]bool),
	}
}

// Start begins the periodic background probing loop in a separate goroutine.
func (hc *HealthChecker) Start() {
	if hc == nil || hc.started {
		return
	}
	hc.started = true
	hc.wg.Add(1)
	go hc.loop()
}

// Stop terminates the background probe loop and waits for active probes to exit.
func (hc *HealthChecker) Stop() {
	if hc == nil || !hc.started {
		return
	}
	close(hc.stopCh)
	hc.wg.Wait()
	hc.started = false
}

// ProbeAll executes a concurrent health probe against all registered providers.
func (hc *HealthChecker) ProbeAll(ctx context.Context) {
	if hc == nil || hc.router == nil {
		return
	}

	hc.router.mu.RLock()
	providers := make(map[string]provider.Provider, len(hc.router.providers))
	for k, v := range hc.router.providers {
		providers[k] = v
	}
	hc.router.mu.RUnlock()

	var wg sync.WaitGroup
	for name, p := range providers {
		wg.Add(1)
		go func(pName string, prov provider.Provider) {
			defer wg.Done()
			alive := hc.probeProvider(ctx, prov)
			hc.statusMu.Lock()
			hc.liveStatuses[pName] = alive
			hc.statusMu.Unlock()

			// If provider recovered and breaker is in open/half-open, record probe success
			if alive {
				if cb, ok := hc.router.GetCircuitBreaker(pName); ok && cb != nil {
					state := cb.GetState()
					if state == StateHalfOpen || state == StateOpen {
						cb.RecordSuccess()
					}
				}
			}
		}(name, p)
	}
	wg.Wait()
}

func (hc *HealthChecker) probeProvider(ctx context.Context, p provider.Provider) bool {
	baseURL := strings.TrimRight(p.GetBaseURL(), "/")
	if baseURL == "" {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return false
	}

	resp, err := hc.client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()

	// Non-5xx HTTP codes indicate the remote endpoint is alive and network reachable
	return resp.StatusCode < 500
}

// IsHealthy returns true if the provider was reachable in the latest health probe.
func (hc *HealthChecker) IsHealthy(name string) bool {
	if hc == nil {
		return true
	}
	hc.statusMu.RLock()
	defer hc.statusMu.RUnlock()
	alive, ok := hc.liveStatuses[name]
	if !ok {
		return true
	}
	return alive
}

func (hc *HealthChecker) loop() {
	defer hc.wg.Done()

	// Execute initial probe immediately
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	hc.ProbeAll(initCtx)
	initCancel()

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-hc.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			hc.ProbeAll(ctx)
			cancel()
		}
	}
}
