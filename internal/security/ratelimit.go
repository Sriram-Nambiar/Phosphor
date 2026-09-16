package security

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TokenBucket implements a thread-safe token bucket rate limiter.
type TokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// NewTokenBucket creates a new token bucket with specified capacity in requests per minute (RPM).
func NewTokenBucket(rpm int) *TokenBucket {
	cap := float64(rpm)
	return &TokenBucket{
		capacity:   cap,
		tokens:     cap,
		refillRate: cap / 60.0,
		lastRefill: time.Now(),
	}
}

// Allow checks whether a request is permitted.
// Returns:
// - allowed: true if token consumed, false if rate limited
// - remaining: remaining whole tokens in bucket
// - retryAfter: duration until at least 1 token is available (if not allowed)
func (tb *TokenBucket) Allow() (bool, int, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	// Replenish tokens up to capacity
	tb.tokens = math.Min(tb.capacity, tb.tokens+(elapsed*tb.refillRate))

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		remaining := int(math.Floor(tb.tokens))
		return true, remaining, 0
	}

	missing := 1.0 - tb.tokens
	retrySec := missing / tb.refillRate
	if retrySec < 1.0 {
		retrySec = 1.0
	}
	return false, 0, time.Duration(math.Ceil(retrySec)) * time.Second
}

// ClientRateLimiter manages token buckets per client key or IP address.
type ClientRateLimiter struct {
	mu           sync.RWMutex
	buckets      map[string]*TokenBucket
	defaultLimit int // RPM
}

// NewClientRateLimiter creates a rate limiter registry.
func NewClientRateLimiter(defaultRPM int) *ClientRateLimiter {
	return &ClientRateLimiter{
		buckets:      make(map[string]*TokenBucket),
		defaultLimit: defaultRPM,
	}
}

// GetBucket retrieves or creates the TokenBucket for a client key and rate limit.
// If limitRPM is <= 0 and defaultLimit is <= 0, returns nil (unlimited).
func (crl *ClientRateLimiter) GetBucket(clientIdentifier string, limitRPM int) *TokenBucket {
	if limitRPM <= 0 {
		limitRPM = crl.defaultLimit
	}
	if limitRPM <= 0 {
		return nil
	}

	crl.mu.RLock()
	tb, exists := crl.buckets[clientIdentifier]
	crl.mu.RUnlock()
	if exists {
		return tb
	}

	crl.mu.Lock()
	defer crl.mu.Unlock()
	if tb, exists = crl.buckets[clientIdentifier]; exists {
		return tb
	}

	tb = NewTokenBucket(limitRPM)
	crl.buckets[clientIdentifier] = tb
	return tb
}

// GetClientIP extracts and validates the client IP address from proxy headers
// (CF-Connecting-IP, True-Client-IP, X-Real-IP, X-Forwarded-For) or RemoteAddr.
func GetClientIP(r *http.Request) string {
	if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
		if ip := cleanIP(cf); ip != "" {
			return ip
		}
	}
	if tci := r.Header.Get("True-Client-IP"); tci != "" {
		if ip := cleanIP(tci); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if ip := cleanIP(xri); ip != "" {
			return ip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for _, part := range strings.Split(xff, ",") {
			if ip := cleanIP(part); ip != "" {
				return ip
			}
		}
	}
	if ip := cleanIP(r.RemoteAddr); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

func cleanIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	return ""
}

// SetRateLimitHeaders sets standard rate limit headers on the response.
func SetRateLimitHeaders(w http.ResponseWriter, limit int, remaining int, retryAfter time.Duration) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
	}
}
