package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type asyncItem struct {
	reqLog    *RequestLog
	failover  *FailoverTrace
	flushDone chan struct{}
}

type DB struct {
	db         *sql.DB
	path       string
	mu         sync.RWMutex
	asyncQueue chan *asyncItem
	wg         sync.WaitGroup
	dropCount  atomic.Uint64
	closed     atomic.Bool
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
	ClientName       string    `json:"client_name,omitempty"`
	CorrelationID    string    `json:"correlation_id,omitempty"`
	SessionID        string    `json:"session_id,omitempty"`
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
    error_msg TEXT,
    client_name TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_requests_created_at ON requests(created_at);
CREATE INDEX IF NOT EXISTS idx_requests_provider ON requests(provider);
CREATE INDEX IF NOT EXISTS idx_requests_client ON requests(client_name);
CREATE INDEX IF NOT EXISTS idx_requests_correlation ON requests(correlation_id);

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

CREATE TABLE IF NOT EXISTS response_cache (
    key TEXT PRIMARY KEY,
    value BLOB NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_response_cache_expires ON response_cache(expires_at);
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

	_, _ = sqlDB.Exec("ALTER TABLE requests ADD COLUMN client_name TEXT NOT NULL DEFAULT '';")
	_, _ = sqlDB.Exec("ALTER TABLE requests ADD COLUMN correlation_id TEXT NOT NULL DEFAULT '';")
	_, _ = sqlDB.Exec("ALTER TABLE requests ADD COLUMN session_id TEXT NOT NULL DEFAULT '';")

	d := &DB{
		db:         sqlDB,
		path:       dbPath,
		asyncQueue: make(chan *asyncItem, 4096),
	}
	d.startWorker(50, 50*time.Millisecond)

	return d, nil
}

func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	if d.closed.CompareAndSwap(false, true) {
		close(d.asyncQueue)
		d.wg.Wait()
	}
	return d.db.Close()
}

// Ping checks if the SQLite database connection is active and responding.
func (d *DB) Ping(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("database not initialized")
	}
	return d.db.PingContext(ctx)
}

// Flush ensures all pending telemetry events in the async queue are written to SQLite.
func (d *DB) Flush(ctx context.Context) error {
	if d == nil || d.closed.Load() {
		return nil
	}
	done := make(chan struct{})
	item := &asyncItem{flushDone: done}
	select {
	case d.asyncQueue <- item:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Vacuum compacts the SQLite database, reclaims freed pages, and executes PRAGMA optimize.
func (d *DB) Vacuum(ctx context.Context) error {
	if d == nil || d.db == nil {
		return errors.New("database not initialized")
	}

	// Flush pending writes first to ensure clean state
	_ = d.Flush(ctx)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.db.ExecContext(ctx, "VACUUM;"); err != nil {
		return fmt.Errorf("failed to vacuum database: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA optimize;"); err != nil {
		return fmt.Errorf("failed to optimize database: %w", err)
	}
	return nil
}

// FileSize returns the size of the database file on disk in bytes.
// If the database is in-memory (:memory:), it returns 0.
func (d *DB) FileSize() (int64, error) {
	if d == nil {
		return 0, errors.New("database not initialized")
	}
	if d.path == "" || d.path == ":memory:" || strings.HasPrefix(d.path, "file::memory:") {
		return 0, nil
	}

	cleanPath := d.path
	if idx := strings.Index(cleanPath, "?"); idx != -1 {
		cleanPath = cleanPath[:idx]
	}

	fi, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}

// QueueDepth returns the current number of pending items in the async queue.
func (d *DB) QueueDepth() int {
	if d == nil {
		return 0
	}
	return len(d.asyncQueue)
}

// DroppedLogsCount returns the total number of logs dropped due to queue backpressure.
func (d *DB) DroppedLogsCount() uint64 {
	if d == nil {
		return 0
	}
	return d.dropCount.Load()
}

func (d *DB) startWorker(batchSize int, flushInterval time.Duration) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		reqBatch := make([]*RequestLog, 0, batchSize)
		failBatch := make([]*FailoverTrace, 0, batchSize)
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		flush := func() {
			if len(reqBatch) == 0 && len(failBatch) == 0 {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = d.LogBatch(ctx, reqBatch, failBatch)
			cancel()
			reqBatch = reqBatch[:0]
			failBatch = failBatch[:0]
		}

		for {
			select {
			case item, ok := <-d.asyncQueue:
				if !ok {
					flush()
					return
				}
				if item.flushDone != nil {
					flush()
					close(item.flushDone)
					continue
				}
				if item.reqLog != nil {
					reqBatch = append(reqBatch, item.reqLog)
				}
				if item.failover != nil {
					failBatch = append(failBatch, item.failover)
				}
				if len(reqBatch) >= batchSize || len(failBatch) >= batchSize {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
	}()
}

// LogBatch inserts multiple request logs and failover traces in a single atomic transaction.
func (d *DB) LogBatch(ctx context.Context, reqs []*RequestLog, failovers []*FailoverTrace) error {
	if len(reqs) == 0 && len(failovers) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin batch transaction: %w", err)
	}
	defer tx.Rollback()

	if len(reqs) > 0 {
		reqStmt, err := tx.PrepareContext(ctx, `
INSERT INTO requests (
    id, created_at, model_requested, provider, model_routed,
    prompt_tokens, completion_tokens, total_tokens, estimated_cost,
    latency_ms, ttft_ms, status_code, stream, error_msg, client_name,
    correlation_id, session_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`)
		if err != nil {
			return fmt.Errorf("failed to prepare batch requests stmt: %w", err)
		}
		defer reqStmt.Close()

		for _, r := range reqs {
			if r.ID == "" {
				r.ID = uuid.New().String()
			}
			if r.CreatedAt.IsZero() {
				r.CreatedAt = time.Now().UTC()
			}
			streamInt := 0
			if r.Stream {
				streamInt = 1
			}
			_, err := reqStmt.ExecContext(ctx,
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
				r.ClientName,
				r.CorrelationID,
				r.SessionID,
			)
			if err != nil {
				return fmt.Errorf("failed to insert batched request log: %w", err)
			}
		}
	}

	if len(failovers) > 0 {
		fStmt, err := tx.PrepareContext(ctx, `
INSERT INTO failover_traces (
    id, request_id, timestamp, from_provider, to_provider, reason, latency_ms
) VALUES (?, ?, ?, ?, ?, ?, ?);
`)
		if err != nil {
			return fmt.Errorf("failed to prepare batch failover stmt: %w", err)
		}
		defer fStmt.Close()

		for _, f := range failovers {
			if f.ID == "" {
				f.ID = uuid.New().String()
			}
			if f.Timestamp.IsZero() {
				f.Timestamp = time.Now().UTC()
			}
			_, err := fStmt.ExecContext(ctx,
				f.ID,
				f.RequestID,
				f.Timestamp.Format(time.RFC3339Nano),
				f.FromProvider,
				f.ToProvider,
				f.Reason,
				f.LatencyMs,
			)
			if err != nil {
				return fmt.Errorf("failed to insert batched failover trace: %w", err)
			}
		}
	}

	return tx.Commit()
}

// EnqueueRequestLog attempts non-blocking enqueue into the async telemetry queue.
func (d *DB) EnqueueRequestLog(r *RequestLog) bool {
	if d == nil || d.closed.Load() {
		return false
	}
	item := &asyncItem{reqLog: r}
	select {
	case d.asyncQueue <- item:
		return true
	default:
		d.dropCount.Add(1)
		return false
	}
}

// EnqueueFailover attempts non-blocking enqueue into the async telemetry queue.
func (d *DB) EnqueueFailover(f *FailoverTrace) bool {
	if d == nil || d.closed.Load() {
		return false
	}
	item := &asyncItem{failover: f}
	select {
	case d.asyncQueue <- item:
		return true
	default:
		d.dropCount.Add(1)
		return false
	}
}

// LogRequest logs a request asynchronously via the batch queue, falling back to sync on full queue.
func (d *DB) LogRequest(ctx context.Context, r *RequestLog) error {
	if d.EnqueueRequestLog(r) {
		return nil
	}
	return d.LogRequestSync(ctx, r)
}

// LogFailover logs a failover trace asynchronously via the batch queue, falling back to sync on full queue.
func (d *DB) LogFailover(ctx context.Context, f *FailoverTrace) error {
	if d.EnqueueFailover(f) {
		return nil
	}
	return d.LogFailoverSync(ctx, f)
}

// LogRequestSync synchronously inserts a request log into SQLite.
func (d *DB) LogRequestSync(ctx context.Context, r *RequestLog) error {
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
    latency_ms, ttft_ms, status_code, stream, error_msg, client_name,
    correlation_id, session_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
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
		r.ClientName,
		r.CorrelationID,
		r.SessionID,
	)
	if err != nil {
		return fmt.Errorf("failed to insert request log: %w", err)
	}

	return nil
}

// LogFailoverSync synchronously inserts a failover trace into SQLite.
func (d *DB) LogFailoverSync(ctx context.Context, f *FailoverTrace) error {
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
	_ = d.Flush(ctx)
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
	_ = d.Flush(ctx)
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
	_ = d.Flush(ctx)
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
	_ = d.Flush(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}

	query := `
SELECT 
    id, created_at, model_requested, provider, model_routed,
    prompt_tokens, completion_tokens, total_tokens, estimated_cost,
    latency_ms, ttft_ms, status_code, stream, COALESCE(error_msg, ''),
    COALESCE(client_name, ''), COALESCE(correlation_id, ''), COALESCE(session_id, '')
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
			&r.ClientName,
			&r.CorrelationID,
			&r.SessionID,
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
	_ = d.Flush(ctx)
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

// GetCachedResponse retrieves a cached response by key. If the entry is expired, it is deleted and returns false.
func (d *DB) GetCachedResponse(ctx context.Context, key string) ([]byte, bool, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT value, expires_at FROM response_cache WHERE key = ? LIMIT 1;`
	row := d.db.QueryRowContext(ctx, query, key)

	var value []byte
	var expiresAtStr string
	if err := row.Scan(&value, &expiresAtStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to query response cache: %w", err)
	}

	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtStr)
	if err == nil && !expiresAt.IsZero() && time.Now().UTC().After(expiresAt) {
		// Asynchronously delete expired entry
		go func(expiredKey string) {
			d.mu.Lock()
			defer d.mu.Unlock()
			_, _ = d.db.Exec("DELETE FROM response_cache WHERE key = ?", expiredKey)
		}(key)
		return nil, false, nil
	}

	return value, true, nil
}

// SetCachedResponse stores or updates a response cache entry with TTL expiration.
func (d *DB) SetCachedResponse(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	query := `
INSERT INTO response_cache (key, value, expires_at, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
    value = excluded.value,
    expires_at = excluded.expires_at,
    created_at = excluded.created_at;`

	_, err := d.db.ExecContext(ctx, query, key, value, expiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("failed to set cached response: %w", err)
	}
	return nil
}

// DeleteCachedResponse deletes a cached response by key.
func (d *DB) DeleteCachedResponse(ctx context.Context, key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := d.db.ExecContext(ctx, "DELETE FROM response_cache WHERE key = ?", key)
	return err
}

// PruneExpiredCache deletes all expired cache entries and returns the count of deleted rows.
func (d *DB) PruneExpiredCache(ctx context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := d.db.ExecContext(ctx, "DELETE FROM response_cache WHERE expires_at != '' AND expires_at < ?", nowStr)
	if err != nil {
		return 0, fmt.Errorf("failed to prune expired cache: %w", err)
	}
	return res.RowsAffected()
}

// ClearCache deletes all entries from the persistent response_cache table.
func (d *DB) ClearCache(ctx context.Context) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	res, err := d.db.ExecContext(ctx, "DELETE FROM response_cache")
	if err != nil {
		return 0, fmt.Errorf("failed to clear response cache: %w", err)
	}
	return res.RowsAffected()
}

// GetClientSpend returns the total estimated cost for a given client since the specified time window.
func (d *DB) GetClientSpend(ctx context.Context, clientName string, since time.Time) (float64, error) {
	_ = d.Flush(ctx)
	d.mu.RLock()
	defer d.mu.RUnlock()

	var spend float64
	query := `SELECT COALESCE(SUM(estimated_cost), 0.0) FROM requests WHERE client_name = ? AND created_at >= ?`
	err := d.db.QueryRowContext(ctx, query, clientName, since.Format(time.RFC3339Nano)).Scan(&spend)
	if err != nil {
		return 0, fmt.Errorf("failed to query client spend: %w", err)
	}
	return spend, nil
}

