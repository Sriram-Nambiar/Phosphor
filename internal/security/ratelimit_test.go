package security

import (
	"net/http/httptest"
	"testing"
)

func TestTokenBucket_RateLimitingAndRefill(t *testing.T) {
	// Bucket with 2 RPM: capacity = 2, refill = 2/60 = 0.033 tokens/sec
	tb := NewTokenBucket(2)

	// First request: should succeed
	allowed, remaining, _ := tb.Allow()
	if !allowed || remaining != 1 {
		t.Errorf("expected allowed=true, remaining=1, got allowed=%v, remaining=%d", allowed, remaining)
	}

	// Second request: should succeed
	allowed, remaining, _ = tb.Allow()
	if !allowed || remaining != 0 {
		t.Errorf("expected allowed=true, remaining=0, got allowed=%v, remaining=%d", allowed, remaining)
	}

	// Third immediate request: should fail with rate limit
	allowed, _, retryAfter := tb.Allow()
	if allowed {
		t.Error("expected third request to be rate limited")
	}
	if retryAfter <= 0 {
		t.Errorf("expected positive retryAfter duration, got %v", retryAfter)
	}
}

func TestClientRateLimiter(t *testing.T) {
	limiter := NewClientRateLimiter(60)

	// Unknown client with default limit
	b1 := limiter.GetBucket("client-1", 0)
	if b1 == nil {
		t.Fatal("expected non-nil bucket for client-1")
	}
	if b1.capacity != 60 {
		t.Errorf("expected capacity 60, got %v", b1.capacity)
	}

	// Custom limit
	b2 := limiter.GetBucket("client-2", 120)
	if b2.capacity != 120 {
		t.Errorf("expected capacity 120, got %v", b2.capacity)
	}

	// Unlimited client
	unlimited := NewClientRateLimiter(0)
	b3 := unlimited.GetBucket("client-3", 0)
	if b3 != nil {
		t.Error("expected nil bucket for unlimited client")
	}
}

func TestGetClientIP(t *testing.T) {
	// X-Forwarded-For
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	if ip := GetClientIP(req1); ip != "203.0.113.195" {
		t.Errorf("expected 203.0.113.195, got %s", ip)
	}

	// X-Real-IP
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Real-IP", "198.51.100.1")
	if ip := GetClientIP(req2); ip != "198.51.100.1" {
		t.Errorf("expected 198.51.100.1, got %s", ip)
	}

	// RemoteAddr
	req3 := httptest.NewRequest("GET", "/", nil)
	req3.RemoteAddr = "192.0.2.1:12345"
	if ip := GetClientIP(req3); ip != "192.0.2.1" {
		t.Errorf("expected 192.0.2.1, got %s", ip)
	}

	// CF-Connecting-IP
	req4 := httptest.NewRequest("GET", "/", nil)
	req4.Header.Set("CF-Connecting-IP", "104.16.132.229")
	req4.Header.Set("X-Forwarded-For", "203.0.113.1")
	if ip := GetClientIP(req4); ip != "104.16.132.229" {
		t.Errorf("expected CF-Connecting-IP priority 104.16.132.229, got %s", ip)
	}

	// True-Client-IP
	req5 := httptest.NewRequest("GET", "/", nil)
	req5.Header.Set("True-Client-IP", "198.51.100.55")
	req5.Header.Set("X-Forwarded-For", "203.0.113.1")
	if ip := GetClientIP(req5); ip != "198.51.100.55" {
		t.Errorf("expected True-Client-IP priority 198.51.100.55, got %s", ip)
	}

	// IPv6 with port
	req6 := httptest.NewRequest("GET", "/", nil)
	req6.Header.Set("X-Forwarded-For", "[2001:db8::1]:8080, 192.0.2.1")
	if ip := GetClientIP(req6); ip != "2001:db8::1" {
		t.Errorf("expected 2001:db8::1, got %s", ip)
	}

	// Invalid XFF skips to valid second entry
	req7 := httptest.NewRequest("GET", "/", nil)
	req7.Header.Set("X-Forwarded-For", "unknown, 198.51.100.99")
	if ip := GetClientIP(req7); ip != "198.51.100.99" {
		t.Errorf("expected valid second entry 198.51.100.99, got %s", ip)
	}
}
