package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOptimizedTransport_Settings(t *testing.T) {
	tr := NewOptimizedTransport()
	if tr.MaxIdleConns != 256 {
		t.Errorf("expected MaxIdleConns=256, got %d", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != 64 {
		t.Errorf("expected MaxIdleConnsPerHost=64, got %d", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != 128 {
		t.Errorf("expected MaxConnsPerHost=128, got %d", tr.MaxConnsPerHost)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("expected IdleConnTimeout=90s, got %v", tr.IdleConnTimeout)
	}
	if !tr.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be true")
	}
}

func TestNewHTTPClient_ConnectionReuse(t *testing.T) {
	client := NewHTTPClient(10 * time.Second)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	for i := 0; i < 5; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		resp.Body.Close()
	}

	if hits != 5 {
		t.Errorf("expected 5 hits on server, got %d", hits)
	}
}
