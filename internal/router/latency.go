package router

import (
	"context"
	"sync"
	"time"

	"github.com/Sriram-Nambiar/Phosphor/internal/db"
)

type LatencyTracker struct {
	mu      sync.RWMutex
	alpha   float64
	metrics map[string]*LatencyStat
}

type LatencyStat struct {
	Provider    string
	Model       string
	EMATTFTMs   float64
	EMALatencyMs float64
	Count       int
	LastUpdated time.Time
}

func NewLatencyTracker(alpha float64) *LatencyTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &LatencyTracker{
		alpha:   alpha,
		metrics: make(map[string]*LatencyStat),
	}
}

func metricKey(provider, model string) string {
	return provider + "::" + model
}

func (lt *LatencyTracker) Record(provider, model string, ttftMs, latencyMs float64) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	key := metricKey(provider, model)
	stat, ok := lt.metrics[key]
	if !ok {
		lt.metrics[key] = &LatencyStat{
			Provider:     provider,
			Model:        model,
			EMATTFTMs:    ttftMs,
			EMALatencyMs: latencyMs,
			Count:        1,
			LastUpdated:  time.Now().UTC(),
		}
		return
	}

	if ttftMs > 0 {
		if stat.EMATTFTMs <= 0 {
			stat.EMATTFTMs = ttftMs
		} else {
			stat.EMATTFTMs = (lt.alpha * ttftMs) + ((1.0 - lt.alpha) * stat.EMATTFTMs)
		}
	}

	if latencyMs > 0 {
		if stat.EMALatencyMs <= 0 {
			stat.EMALatencyMs = latencyMs
		} else {
			stat.EMALatencyMs = (lt.alpha * latencyMs) + ((1.0 - lt.alpha) * stat.EMALatencyMs)
		}
	}

	stat.Count++
	stat.LastUpdated = time.Now().UTC()
}

func (lt *LatencyTracker) GetTTFT(provider, model string) float64 {
	lt.mu.RLock()
	defer lt.mu.RUnlock()

	key := metricKey(provider, model)
	if stat, ok := lt.metrics[key]; ok && stat.EMATTFTMs > 0 {
		return stat.EMATTFTMs
	}
	// Fallback check provider-wide
	for k, stat := range lt.metrics {
		if stat.Provider == provider && stat.EMATTFTMs > 0 {
			_ = k
			return stat.EMATTFTMs
		}
	}
	return 250.0 // Default baseline TTFT estimate (250ms)
}

func (lt *LatencyTracker) GetLatency(provider, model string) float64 {
	lt.mu.RLock()
	defer lt.mu.RUnlock()

	key := metricKey(provider, model)
	if stat, ok := lt.metrics[key]; ok && stat.EMALatencyMs > 0 {
		return stat.EMALatencyMs
	}
	return 500.0 // Default baseline latency estimate (500ms)
}

// LoadFromDB preloads historic metrics from SQLite database on startup
func (lt *LatencyTracker) LoadFromDB(ctx context.Context, database *db.DB) error {
	if database == nil {
		return nil
	}

	metrics, err := database.GetProviderMetrics(ctx)
	if err != nil {
		return err
	}

	lt.mu.Lock()
	defer lt.mu.Unlock()

	for _, m := range metrics {
		key := metricKey(m.Provider, m.Model)
		lt.metrics[key] = &LatencyStat{
			Provider:     m.Provider,
			Model:        m.Model,
			EMATTFTMs:    m.EMATTFTMs,
			EMALatencyMs: m.EMALatencyMs,
			Count:        m.TotalRequests,
			LastUpdated:  m.LastUpdated,
		}
	}
	return nil
}
