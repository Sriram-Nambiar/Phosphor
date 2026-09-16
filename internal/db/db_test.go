package db

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDB_Initialization(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize in-memory db: %v", err)
	}
	defer d.Close()

	if d.db == nil {
		t.Fatal("expected non-nil sql.DB")
	}

	if err := d.Ping(context.Background()); err != nil {
		t.Fatalf("expected ping to succeed: %v", err)
	}
}

func TestDB_LogRequestAndRecent(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	req := &RequestLog{
		ModelRequested:   "gpt-4o",
		Provider:         "openai",
		ModelRouted:      "gpt-4o-2024-08-06",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		EstimatedCost:    0.00125,
		LatencyMs:        450.5,
		TTFTMs:           120.3,
		StatusCode:       200,
		Stream:           true,
	}

	if err := d.LogRequest(ctx, req); err != nil {
		t.Fatalf("failed to log request: %v", err)
	}

	recent, err := d.GetRecentRequests(ctx, 10)
	if err != nil {
		t.Fatalf("failed to get recent requests: %v", err)
	}

	if len(recent) != 1 {
		t.Fatalf("expected 1 recent request, got %d", len(recent))
	}

	r := recent[0]
	if r.ModelRequested != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", r.ModelRequested)
	}
	if r.Provider != "openai" {
		t.Errorf("expected provider openai, got %s", r.Provider)
	}
	if !r.Stream {
		t.Error("expected stream to be true")
	}
	if r.PromptTokens != 100 || r.CompletionTokens != 50 || r.TotalTokens != 150 {
		t.Errorf("token counts mismatch: %d, %d, %d", r.PromptTokens, r.CompletionTokens, r.TotalTokens)
	}
}

func TestDB_FailoverTraces(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	trace := &FailoverTrace{
		RequestID:    "req-123",
		FromProvider: "openai",
		ToProvider:   "groq",
		Reason:       "HTTP 429: rate limit exceeded",
		LatencyMs:    140.0,
	}

	if err := d.LogFailover(ctx, trace); err != nil {
		t.Fatalf("failed to log failover: %v", err)
	}

	recent, err := d.GetRecentFailovers(ctx, 10)
	if err != nil {
		t.Fatalf("failed to get recent failovers: %v", err)
	}

	if len(recent) != 1 {
		t.Fatalf("expected 1 recent failover, got %d", len(recent))
	}

	if recent[0].FromProvider != "openai" || recent[0].ToProvider != "groq" {
		t.Errorf("unexpected failover providers: from %s to %s", recent[0].FromProvider, recent[0].ToProvider)
	}
	if recent[0].Reason != "HTTP 429: rate limit exceeded" {
		t.Errorf("unexpected reason: %s", recent[0].Reason)
	}
}

func TestDB_UpdateLatencyEMA(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// First observation
	err = d.UpdateLatencyEMA(ctx, "groq", "llama-3.3-70b", 100.0, 300.0, 0.2, false)
	if err != nil {
		t.Fatalf("first update failed: %v", err)
	}

	m, err := d.GetProviderMetric(ctx, "groq", "llama-3.3-70b")
	if err != nil {
		t.Fatalf("failed to get metric: %v", err)
	}
	if m == nil {
		t.Fatal("expected metric, got nil")
	}
	if m.EMATTFTMs != 100.0 || m.EMALatencyMs != 300.0 {
		t.Errorf("expected initial EMA 100/300, got %f/%f", m.EMATTFTMs, m.EMALatencyMs)
	}
	if m.TotalRequests != 1 || m.TotalFailures != 0 {
		t.Errorf("expected reqs=1, fails=0, got reqs=%d, fails=%d", m.TotalRequests, m.TotalFailures)
	}

	// Second observation with failure
	// newEMA = 0.2 * 200 + 0.8 * 100 = 40 + 80 = 120.0
	// latencyEMA = 0.2 * 500 + 0.8 * 300 = 100 + 240 = 340.0
	err = d.UpdateLatencyEMA(ctx, "groq", "llama-3.3-70b", 200.0, 500.0, 0.2, true)
	if err != nil {
		t.Fatalf("second update failed: %v", err)
	}

	m, err = d.GetProviderMetric(ctx, "groq", "llama-3.3-70b")
	if err != nil {
		t.Fatalf("failed to get metric: %v", err)
	}
	if m.EMATTFTMs != 120.0 {
		t.Errorf("expected updated EMA TTFT 120.0, got %f", m.EMATTFTMs)
	}
	if m.EMALatencyMs != 340.0 {
		t.Errorf("expected updated EMA Latency 340.0, got %f", m.EMALatencyMs)
	}
	if m.TotalRequests != 2 || m.TotalFailures != 1 {
		t.Errorf("expected reqs=2, fails=1, got reqs=%d, fails=%d", m.TotalRequests, m.TotalFailures)
	}
}

func TestDB_GetAggregateStats(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	_ = d.LogRequest(ctx, &RequestLog{
		ModelRequested:   "gpt-4o",
		Provider:         "openai",
		ModelRouted:      "gpt-4o",
		PromptTokens:     100,
		CompletionTokens: 200,
		TotalTokens:      300,
		EstimatedCost:    0.005,
		LatencyMs:        500.0,
		TTFTMs:           150.0,
		StatusCode:       200,
	})

	_ = d.LogRequest(ctx, &RequestLog{
		ModelRequested:   "llama-3.3-70b",
		Provider:         "groq",
		ModelRouted:      "llama-3.3-70b",
		PromptTokens:     50,
		CompletionTokens: 100,
		TotalTokens:      150,
		EstimatedCost:    0.001,
		LatencyMs:        200.0,
		TTFTMs:           50.0,
		StatusCode:       200,
	})

	_ = d.LogFailover(ctx, &FailoverTrace{
		RequestID:    "req-1",
		FromProvider: "openai",
		ToProvider:   "groq",
		Reason:       "HTTP 429",
		LatencyMs:    50.0,
	})

	stats, err := d.GetAggregateStats(ctx)
	if err != nil {
		t.Fatalf("failed to get aggregate stats: %v", err)
	}

	if stats.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", stats.TotalRequests)
	}
	if stats.TotalTokens != 450 {
		t.Errorf("expected 450 total tokens, got %d", stats.TotalTokens)
	}
	if stats.TotalCost < 0.0059 || stats.TotalCost > 0.0061 {
		t.Errorf("expected ~0.006 cost, got %f", stats.TotalCost)
	}
	if stats.TotalFailovers != 1 {
		t.Errorf("expected 1 failover, got %d", stats.TotalFailovers)
	}
	if len(stats.ProviderStats) != 2 {
		t.Errorf("expected 2 provider stats entries, got %d", len(stats.ProviderStats))
	}
	if stats.ProviderStats["openai"].Requests != 1 || stats.ProviderStats["groq"].Requests != 1 {
		t.Errorf("provider breakdown mismatch: %+v", stats.ProviderStats)
	}
}

func TestDB_AsyncQueue_BatchPersistence(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// Enqueue 120 requests (larger than batchSize 50)
	for i := 0; i < 120; i++ {
		ok := d.EnqueueRequestLog(&RequestLog{
			ModelRequested: "gpt-4o",
			Provider:       "openai",
			ModelRouted:    "gpt-4o",
			StatusCode:     200,
		})
		if !ok {
			t.Fatalf("failed to enqueue request log %d", i)
		}
	}

	// Flush to commit all batches
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	stats, err := d.GetAggregateStats(ctx)
	if err != nil {
		t.Fatalf("failed to query stats: %v", err)
	}
	if stats.TotalRequests != 120 {
		t.Errorf("expected 120 total requests in DB, got %d", stats.TotalRequests)
	}
}

func TestDB_LogBatch(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	reqs := []*RequestLog{
		{ModelRequested: "claude-3-5-sonnet", Provider: "anthropic", ModelRouted: "claude-3-5-sonnet", StatusCode: 200},
		{ModelRequested: "llama3.2", Provider: "ollama", ModelRouted: "llama3.2", StatusCode: 200},
	}
	failovers := []*FailoverTrace{
		{RequestID: "req-batch-1", FromProvider: "anthropic", ToProvider: "ollama", Reason: "503 service unavailable"},
	}

	if err := d.LogBatch(ctx, reqs, failovers); err != nil {
		t.Fatalf("LogBatch failed: %v", err)
	}

	stats, err := d.GetAggregateStats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.TotalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", stats.TotalRequests)
	}
	if stats.TotalFailovers != 1 {
		t.Errorf("expected 1 failover, got %d", stats.TotalFailovers)
	}
}

func TestDB_ZeroLossQueueDraining_OnClose(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "draining_test.db")

	d, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}

	const totalLogs = 150
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < totalLogs/5; j++ {
				_ = d.EnqueueRequestLog(&RequestLog{
					ModelRequested: "gpt-4o",
					Provider:       "openai",
					ModelRouted:    "gpt-4o",
					StatusCode:     200,
				})
			}
		}(i)
	}
	wg.Wait()

	// Close database immediately without calling Flush manually
	if err := d.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	// Reopen database and verify all 150 logs were drained and persisted
	reopened, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer reopened.Close()

	stats, err := reopened.GetAggregateStats(context.Background())
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.TotalRequests != totalLogs {
		t.Errorf("expected all %d requests drained on Close, got %d", totalLogs, stats.TotalRequests)
	}
}

func TestDB_BackpressureDropMetrics(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer d.Close()

	if d.QueueDepth() < 0 {
		t.Errorf("expected non-negative queue depth")
	}

	// Artificially close queue to force backpressure drops
	d.closed.Store(true)

	ok := d.EnqueueRequestLog(&RequestLog{ModelRequested: "gpt-4o"})
	if ok {
		t.Error("expected enqueue to return false when db is closed")
	}
	// Verify DroppedLogsCount can be read
	_ = d.DroppedLogsCount()
}

func TestDB_ResponseCache(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// 1. Get nonexistent key
	val, found, err := d.GetCachedResponse(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found || val != nil {
		t.Errorf("expected found=false for nonexistent key")
	}

	// 2. Set with TTL
	payload := []byte(`{"id":"chatcmpl-cached","choices":[{"message":{"content":"cached response"}}]}`)
	if err := d.SetCachedResponse(ctx, "prompt-key-1", payload, 1*time.Hour); err != nil {
		t.Fatalf("failed to set cached response: %v", err)
	}

	// 3. Get existing key
	val, found, err = d.GetCachedResponse(ctx, "prompt-key-1")
	if err != nil {
		t.Fatalf("failed to get cached response: %v", err)
	}
	if !found || string(val) != string(payload) {
		t.Errorf("expected to find payload, found=%v, val=%s", found, string(val))
	}

	// 4. Expiration check
	shortPayload := []byte(`{"content":"short-lived"}`)
	if err := d.SetCachedResponse(ctx, "short-key", shortPayload, 20*time.Millisecond); err != nil {
		t.Fatalf("failed to set short-lived cache: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	val, found, err = d.GetCachedResponse(ctx, "short-key")
	if err != nil {
		t.Fatalf("unexpected error on expired get: %v", err)
	}
	if found {
		t.Errorf("expected short-key to be expired and not found")
	}

	// 5. Prune expired cache
	pruned, err := d.PruneExpiredCache(ctx)
	if err != nil {
		t.Fatalf("failed to prune expired cache: %v", err)
	}
	_ = pruned

	// 6. Delete key
	if err := d.DeleteCachedResponse(ctx, "prompt-key-1"); err != nil {
		t.Fatalf("failed to delete key: %v", err)
	}
	val, found, _ = d.GetCachedResponse(ctx, "prompt-key-1")
	if found {
		t.Errorf("expected key to be deleted")
	}
}

func TestDB_ClientSpend(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// Insert requests for client-A and client-B
	_ = d.LogRequestSync(ctx, &RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "openai",
		ModelRouted:    "gpt-4o",
		EstimatedCost:  0.05,
		StatusCode:     200,
		ClientName:     "client-A",
		CreatedAt:      now.Add(-10 * time.Minute),
	})

	_ = d.LogRequestSync(ctx, &RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "openai",
		ModelRouted:    "gpt-4o",
		EstimatedCost:  0.03,
		StatusCode:     200,
		ClientName:     "client-A",
		CreatedAt:      now.Add(-5 * time.Minute),
	})

	_ = d.LogRequestSync(ctx, &RequestLog{
		ModelRequested: "claude-3-5-sonnet",
		Provider:       "anthropic",
		ModelRouted:    "claude-3-5-sonnet",
		EstimatedCost:  0.10,
		StatusCode:     200,
		ClientName:     "client-B",
		CreatedAt:      now.Add(-2 * time.Minute),
	})

	// Check spend for client-A in last hour
	spendA, err := d.GetClientSpend(ctx, "client-A", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error querying spendA: %v", err)
	}
	if spendA < 0.079 || spendA > 0.081 {
		t.Errorf("expected spend ~0.08 for client-A, got %f", spendA)
	}

	// Check spend for client-A in last 7 minutes (should only include 0.03)
	spendA_recent, err := d.GetClientSpend(ctx, "client-A", now.Add(-7*time.Minute))
	if err != nil {
		t.Fatalf("unexpected error querying spendA_recent: %v", err)
	}
	if spendA_recent < 0.029 || spendA_recent > 0.031 {
		t.Errorf("expected spend ~0.03 for client-A recent, got %f", spendA_recent)
	}

	// Check spend for client-B
	spendB, err := d.GetClientSpend(ctx, "client-B", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error querying spendB: %v", err)
	}
	if spendB < 0.099 || spendB > 0.101 {
		t.Errorf("expected spend ~0.10 for client-B, got %f", spendB)
	}

	// Non-existent client
	spendC, err := d.GetClientSpend(ctx, "client-C", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error querying spendC: %v", err)
	}
	if spendC != 0.0 {
		t.Errorf("expected 0.0 for client-C, got %f", spendC)
	}
}

func TestDB_VacuumAndFileSize(t *testing.T) {
	ctx := context.Background()

	// 1. In-memory database
	memDB, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer memDB.Close()

	if err := memDB.Vacuum(ctx); err != nil {
		t.Fatalf("expected memory vacuum to succeed: %v", err)
	}
	size, err := memDB.FileSize()
	if err != nil {
		t.Fatalf("unexpected error getting memory db file size: %v", err)
	}
	if size != 0 {
		t.Errorf("expected size 0 for memory db, got %d", size)
	}

	// 2. File-based database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_vacuum.db")

	fileDB, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to open file db: %v", err)
	}
	defer fileDB.Close()

	// Insert some records
	for i := 0; i < 20; i++ {
		_ = fileDB.LogRequest(ctx, &RequestLog{
			ModelRequested: "gpt-4o",
			Provider:       "openai",
			ModelRouted:    "gpt-4o",
			PromptTokens:   50,
			StatusCode:     200,
		})
	}
	_ = fileDB.Flush(ctx)

	if err := fileDB.Vacuum(ctx); err != nil {
		t.Fatalf("expected file db vacuum to succeed: %v", err)
	}

	fileSize, err := fileDB.FileSize()
	if err != nil {
		t.Fatalf("expected valid file size: %v", err)
	}
	if fileSize <= 0 {
		t.Errorf("expected positive file size, got %d", fileSize)
	}
}

func TestDB_CorrelationAndSessionIDs(t *testing.T) {
	ctx := context.Background()
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer d.Close()

	req := &RequestLog{
		ModelRequested: "gpt-4o",
		Provider:       "openai",
		ModelRouted:    "gpt-4o",
		PromptTokens:   20,
		StatusCode:     200,
		ClientName:     "corp-client",
		CorrelationID:  "corr-987654321",
		SessionID:      "sess-123456789",
	}

	if err := d.LogRequest(ctx, req); err != nil {
		t.Fatalf("failed to log request: %v", err)
	}
	_ = d.Flush(ctx)

	recent, err := d.GetRecentRequests(ctx, 5)
	if err != nil {
		t.Fatalf("failed to get recent requests: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent request, got %d", len(recent))
	}

	if recent[0].CorrelationID != "corr-987654321" {
		t.Errorf("expected CorrelationID 'corr-987654321', got %q", recent[0].CorrelationID)
	}
	if recent[0].SessionID != "sess-123456789" {
		t.Errorf("expected SessionID 'sess-123456789', got %q", recent[0].SessionID)
	}
	if recent[0].ClientName != "corp-client" {
		t.Errorf("expected ClientName 'corp-client', got %q", recent[0].ClientName)
	}
}

func TestDB_DegradedStateAndErrorTracking(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	if d.IsDegraded() {
		t.Errorf("expected initially not degraded")
	}
	if d.WriteErrorsCount() != 0 {
		t.Errorf("expected 0 write errors, got %d", d.WriteErrorsCount())
	}

	// Record consecutive simulated write errors
	testErr := fmt.Errorf("disk I/O failure")
	d.recordWriteError(testErr)
	d.recordWriteError(testErr)
	if d.IsDegraded() {
		t.Errorf("expected not degraded after only 2 errors")
	}

	d.recordWriteError(testErr)
	if !d.IsDegraded() {
		t.Errorf("expected degraded after 3 consecutive errors")
	}
	if d.WriteErrorsCount() != 3 {
		t.Errorf("expected 3 write errors, got %d", d.WriteErrorsCount())
	}
	if d.LastWriteError() != testErr.Error() {
		t.Errorf("expected last write error %q, got %q", testErr.Error(), d.LastWriteError())
	}

	// Success recovers degraded state
	d.recordWriteSuccess()
	if d.IsDegraded() {
		t.Errorf("expected degraded state cleared on success")
	}

	// Test manual SetDegraded
	d.SetDegraded(true)
	if !d.IsDegraded() {
		t.Errorf("expected degraded to be true after SetDegraded(true)")
	}
	d.SetDegraded(false)
	if d.IsDegraded() {
		t.Errorf("expected degraded to be false after SetDegraded(false)")
	}
}

func TestDB_LatencyPercentiles(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to initialize db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// Empty database
	emptyPct, err := d.GetLatencyPercentiles(ctx)
	if err != nil {
		t.Fatalf("expected nil error on empty db: %v", err)
	}
	if emptyPct.Count != 0 || emptyPct.P50 != 0 {
		t.Errorf("expected zero percentiles on empty db, got %+v", emptyPct)
	}

	// Insert 10 requests with controlled latencies: 100, 200, ..., 1000 ms
	for i := 1; i <= 10; i++ {
		provider := "p1"
		if i > 5 {
			provider = "p2"
		}
		_ = d.LogRequestSync(ctx, &RequestLog{
			ModelRequested: "gpt-4o",
			Provider:       provider,
			ModelRouted:    "gpt-4o",
			PromptTokens:   10,
			StatusCode:     200,
			LatencyMs:      float64(i * 100),
		})
	}

	overall, err := d.GetLatencyPercentiles(ctx)
	if err != nil {
		t.Fatalf("failed to get overall percentiles: %v", err)
	}
	if overall.Count != 10 {
		t.Fatalf("expected 10 records, got %d", overall.Count)
	}
	// With 10 items (index 0..9):
	// p50: index int(9 * 0.50) = 4 -> 500ms
	// p90: index int(9 * 0.90) = 8 -> 900ms
	// p99: index int(9 * 0.99) = 8 -> 900ms
	if overall.P50 != 500.0 {
		t.Errorf("expected p50 500.0, got %.2f", overall.P50)
	}
	if overall.P90 != 900.0 {
		t.Errorf("expected p90 900.0, got %.2f", overall.P90)
	}

	provMap, err := d.GetProviderLatencyPercentiles(ctx)
	if err != nil {
		t.Fatalf("failed to get provider percentiles: %v", err)
	}
	if len(provMap) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(provMap))
	}
	if provMap["p1"].Count != 5 || provMap["p2"].Count != 5 {
		t.Errorf("unexpected provider counts: %+v", provMap)
	}
}




