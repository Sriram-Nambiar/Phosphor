package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
	mu sync.RWMutex
}

type RequestLog struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	ModelRequested   string    `json:"model_requested"`
	Provider         string    `json:"provider"`
	ModelRouted      string    `json:"model_routed"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	EstimatedCost    float64   `json:"estimated_cost"`
	LatencyMs        float64   `json:"latency_ms"`
	TTFTMs           float64   `json:"ttft_ms"`
	StatusCode       int       `json:"status_code"`
	Stream           bool      `json:"stream"`
	ErrorMsg         string    `json:"error_msg,omitempty"`
}

type FailoverTrace struct {
	ID           string    `json:"id"`
	RequestID    string    `json:"request_id"`
	Timestamp    time.Time `json:"timestamp"`
	FromProvider string    `json:"from_provider"`
	ToProvider   string    `json:"to_provider"`
	Reason       string    `json:"reason"`
	LatencyMs    float64   `json:"latency_ms"`
}

type ProviderMetric struct {
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	EMATTFTMs     float64   `json:"ema_ttft_ms"`
	EMALatencyMs  float64   `json:"ema_latency_ms"`
	TotalRequests int       `json:"total_requests"`
	TotalFailures int       `json:"total_failures"`
	LastUpdated   time.Time `json:"last_updated"`
}

type AggregateStats struct {
	TotalRequests    int                     `json:"total_requests"`
	TotalPromptTok   int                     `json:"total_prompt_tokens"`
	TotalCompTok     int                     `json:"total_completion_tokens"`
	TotalTokens      int                     `json:"total_tokens"`
	TotalCost        float64                 `json:"total_cost"`
	AvgLatencyMs     float64                 `json:"avg_latency_ms"`
	AvgTTFTMs        float64                 `json:"avg_ttft_ms"`
	ProviderStats    map[string]ProviderStat `json:"provider_stats"`
	TotalFailovers   int                     `json:"total_failovers"`
}

type ProviderStat struct {
	Requests      int     `json:"requests"`
	Tokens        int     `json:"tokens"`
	Cost          float64 `json:"cost"`
	Failures      int     `json:"failures"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
	AvgTTFTMs     float64 `json:"avg_ttft_ms"`
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    model_requested TEXT NOT NULL,
    provider TEXT NOT NULL,
    model_routed TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL,
    estimated_cost REAL NOT NULL,
    latency_ms REAL NOT NULL,
    ttft_ms REAL NOT NULL,
    status_code INTEGER NOT NULL,
    stream INTEGER NOT NULL,
    error_msg TEXT
);

CREATE INDEX IF NOT EXISTS idx_requests_created_at ON requests(created_at);
CREATE INDEX IF NOT EXISTS idx_requests_provider ON requests(provider);

CREATE TABLE IF NOT EXISTS failover_traces (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    from_provider TEXT NOT NULL,
    to_provider TEXT NOT NULL,
    reason TEXT NOT NULL,
    latency_ms REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_failovers_req_id ON failover_traces(request_id);

CREATE TABLE IF NOT EXISTS provider_metrics (
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    ema_ttft_ms REAL NOT NULL,
    ema_latency_ms REAL NOT NULL,
    total_requests INTEGER NOT NULL DEFAULT 0,
    total_failures INTEGER NOT NULL DEFAULT 0,
    last_updated TEXT NOT NULL,
    PRIMARY KEY (provider, model)
);
`

// New initializes a new SQLite connection using modernc.org/sqlite.
func New(dbPath string) (*DB, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}

	if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file:") {
		// Expand tilde if present
		if strings.HasPrefix(dbPath, "~") {
			homeDir, err := os.UserHomeDir()
			if err == nil {
				dbPath = filepath.Join(homeDir, strings.TrimPrefix(dbPath, "~"))
			}
		}

		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	dsn := dbPath
	if dbPath != ":memory:" && !strings.Contains(dbPath, "?") {
		// Add pragmas for performance and concurrency
		dsn = fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// SQLite handles one writer at a time well with WAL; pool size:
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to apply database schema: %w", err)
	}

	return &DB{db: sqlDB}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) LogRequest(ctx context.Context, r *RequestLog) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}

	query := `
INSERT INTO requests (
    id, created_at, model_requested, provider, model_routed,
    prompt_tokens, completion_tokens, total_tokens, estimated_cost,
    latency_ms, ttft_ms, status_code, stream, error_msg
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`
	streamInt := 0
	if r.Stream {
		streamInt = 1
	}

	_, err := d.db.ExecContext(ctx, query,
		r.ID,
		r.CreatedAt.Format(time.RFC3339Nano),
		r.ModelRequested,
		r.Provider,
		r.ModelRouted,
		r.PromptTokens,
		r.CompletionTokens,
		r.TotalTokens,
		r.EstimatedCost,
		r.LatencyMs,
		r.TTFTMs,
		r.StatusCode,
		streamInt,
		r.ErrorMsg,
	)
	if err != nil {
		return fmt.Errorf("failed to insert request log: %w", err)
	}

	return nil
}

func (d *DB) LogFailover(ctx context.Context, f *FailoverTrace) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	if f.Timestamp.IsZero() {
		f.Timestamp = time.Now().UTC()
	}

	query := `
INSERT INTO failover_traces (
    id, request_id, timestamp, from_provider, to_provider, reason, latency_ms
) VALUES (?, ?, ?, ?, ?, ?, ?);
`
	_, err := d.db.ExecContext(ctx, query,
		f.ID,
		f.RequestID,
		f.Timestamp.Format(time.RFC3339Nano),
		f.FromProvider,
		f.ToProvider,
		f.Reason,
		f.LatencyMs,
	)
	if err != nil {
		return fmt.Errorf("failed to insert failover trace: %w", err)
	}

	return nil
}

// UpdateLatencyEMA updates rolling exponential moving averages of TTFT and latency.
// alpha is the smoothing factor between 0.0 and 1.0 (typically 0.2).
func (d *DB) UpdateLatencyEMA(ctx context.Context, provider, model string, ttftMs, latencyMs float64, alpha float64, isFailure bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}

	now := time.Now().UTC().Format(time.RFC3339)

	var currentTTFT, currentLatency float64
	var totalReqs, totalFails int

	querySelect := `SELECT ema_ttft_ms, ema_latency_ms, total_requests, total_failures FROM provider_metrics WHERE provider = ? AND model = ?`
	row := d.db.QueryRowContext(ctx, querySelect, provider, model)
	err := row.Scan(&currentTTFT, &currentLatency, &totalReqs, &totalFails)

	failInc := 0
	if isFailure {
		failInc = 1
	}

	if err == sql.ErrNoRows {
		// Insert new record
		newTTFT := ttftMs
		newLatency := latencyMs
		queryInsert := `
INSERT INTO provider_metrics (provider, model, ema_ttft_ms, ema_latency_ms, total_requests, total_failures, last_updated)
VALUES (?, ?, ?, ?, 1, ?, ?);`
		_, err = d.db.ExecContext(ctx, queryInsert, provider, model, newTTFT, newLatency, failInc, now)
		return err
	} else if err != nil {
		return fmt.Errorf("failed to query provider metrics: %w", err)
	}

	newTTFT := currentTTFT
	if ttftMs > 0 {
		if currentTTFT <= 0 {
			newTTFT = ttftMs
		} else {
			newTTFT = (alpha * ttftMs) + ((1.0 - alpha) * currentTTFT)
		}
	}

	newLatency := currentLatency
	if latencyMs > 0 {
		if currentLatency <= 0 {
			newLatency = latencyMs
		} else {
			newLatency = (alpha * latencyMs) + ((1.0 - alpha) * currentLatency)
		}
	}

	queryUpdate := `
UPDATE provider_metrics
SET ema_ttft_ms = ?, ema_latency_ms = ?, total_requests = total_requests + 1, total_failures = total_failures + ?, last_updated = ?
WHERE provider = ? AND model = ?;`
	_, err = d.db.ExecContext(ctx, queryUpdate, newTTFT, newLatency, failInc, now, provider, model)
	return err
}

func (d *DB) GetProviderMetrics(ctx context.Context) ([]ProviderMetric, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT provider, model, ema_ttft_ms, ema_latency_ms, total_requests, total_failures, last_updated FROM provider_metrics ORDER BY provider, model`
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query provider metrics: %w", err)
	}
	defer rows.Close()

	var metrics []ProviderMetric
	for rows.Next() {
		var m ProviderMetric
		var lastUpdatedStr string
		if err := rows.Scan(&m.Provider, &m.Model, &m.EMATTFTMs, &m.EMALatencyMs, &m.TotalRequests, &m.TotalFailures, &lastUpdatedStr); err != nil {
			return nil, err
		}
		m.LastUpdated, _ = time.Parse(time.RFC3339, lastUpdatedStr)
		metrics = append(metrics, m)
	}

	return metrics, rows.Err()
}

func (d *DB) GetProviderMetric(ctx context.Context, provider, model string) (*ProviderMetric, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT provider, model, ema_ttft_ms, ema_latency_ms, total_requests, total_failures, last_updated FROM provider_metrics WHERE provider = ? AND model = ?`
	row := d.db.QueryRowContext(ctx, query, provider, model)

	var m ProviderMetric
	var lastUpdatedStr string
	if err := row.Scan(&m.Provider, &m.Model, &m.EMATTFTMs, &m.EMALatencyMs, &m.TotalRequests, &m.TotalFailures, &lastUpdatedStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.LastUpdated, _ = time.Parse(time.RFC3339, lastUpdatedStr)
	return &m, nil
}

func (d *DB) GetAggregateStats(ctx context.Context) (*AggregateStats, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	stats := &AggregateStats{
		ProviderStats: make(map[string]ProviderStat),
	}

	// Overall totals
	totalQuery := `
SELECT 
    COUNT(*),
    COALESCE(SUM(prompt_tokens), 0),
    COALESCE(SUM(completion_tokens), 0),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(SUM(estimated_cost), 0.0),
    COALESCE(AVG(latency_ms), 0.0),
    COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms ELSE NULL END), 0.0)
FROM requests;`

	err := d.db.QueryRowContext(ctx, totalQuery).Scan(
		&stats.TotalRequests,
		&stats.TotalPromptTok,
		&stats.TotalCompTok,
		&stats.TotalTokens,
		&stats.TotalCost,
		&stats.AvgLatencyMs,
		&stats.AvgTTFTMs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query total aggregate stats: %w", err)
	}

	// Failovers count
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM failover_traces;`).Scan(&stats.TotalFailovers)

	// Per-provider breakdown
	provQuery := `
SELECT 
    provider,
    COUNT(*),
    COALESCE(SUM(total_tokens), 0),
    COALESCE(SUM(estimated_cost), 0.0),
    COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0),
    COALESCE(AVG(latency_ms), 0.0),
    COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms ELSE NULL END), 0.0)
FROM requests
GROUP BY provider;`

	rows, err := d.db.QueryContext(ctx, provQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query provider stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var prov string
		var ps ProviderStat
		if err := rows.Scan(&prov, &ps.Requests, &ps.Tokens, &ps.Cost, &ps.Failures, &ps.AvgLatencyMs, &ps.AvgTTFTMs); err != nil {
			return nil, err
		}
		stats.ProviderStats[prov] = ps
	}

	return stats, rows.Err()
}

func (d *DB) GetRecentRequests(ctx context.Context, limit int) ([]RequestLog, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	query := `
SELECT 
    id, created_at, model_requested, provider, model_routed,
    prompt_tokens, completion_tokens, total_tokens, estimated_cost,
    latency_ms, ttft_ms, status_code, stream, COALESCE(error_msg, '')
FROM requests
ORDER BY created_at DESC
LIMIT ?;`

	rows, err := d.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent requests: %w", err)
	}
	defer rows.Close()

	var logs []RequestLog
	for rows.Next() {
		var r RequestLog
		var createdAtStr string
		var streamInt int
		if err := rows.Scan(
			&r.ID,
			&createdAtStr,
			&r.ModelRequested,
			&r.Provider,
			&r.ModelRouted,
			&r.PromptTokens,
			&r.CompletionTokens,
			&r.TotalTokens,
			&r.EstimatedCost,
			&r.LatencyMs,
			&r.TTFTMs,
			&r.StatusCode,
			&streamInt,
			&r.ErrorMsg,
		); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		r.Stream = streamInt == 1
		logs = append(logs, r)
	}

	return logs, rows.Err()
}

func (d *DB) GetRecentFailovers(ctx context.Context, limit int) ([]FailoverTrace, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	query := `
SELECT id, request_id, timestamp, from_provider, to_provider, reason, latency_ms
FROM failover_traces
ORDER BY timestamp DESC
LIMIT ?;`

	rows, err := d.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent failovers: %w", err)
	}
	defer rows.Close()

	var traces []FailoverTrace
	for rows.Next() {
		var f FailoverTrace
		var tsStr string
		if err := rows.Scan(&f.ID, &f.RequestID, &tsStr, &f.FromProvider, &f.ToProvider, &f.Reason, &f.LatencyMs); err != nil {
			return nil, err
		}
		f.Timestamp, _ = time.Parse(time.RFC3339Nano, tsStr)
		traces = append(traces, f)
	}

	return traces, rows.Err()
}
