package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPFilter_AllowlistExactAndCIDR(t *testing.T) {
	filter, err := NewIPFilter([]string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"}, nil)
	if err != nil {
		t.Fatalf("failed to create filter: %v", err)
	}

	tests := []struct {
		ip      string
		allowed bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.2", false},
		{"10.1.2.3", true},
		{"10.255.255.255", true},
		{"11.0.0.1", false},
		{"192.168.1.1", false},
		{"2001:db8::1", true},
		{"2001:db9::1", false},
	}

	for _, tc := range tests {
		got := filter.IsAllowed(tc.ip)
		if got != tc.allowed {
			t.Errorf("IsAllowed(%q) = %v, expected %v", tc.ip, got, tc.allowed)
		}
	}
}

func TestIPFilter_DenylistExactAndCIDR(t *testing.T) {
	filter, err := NewIPFilter(nil, []string{"192.168.1.50", "172.16.0.0/12"})
	if err != nil {
		t.Fatalf("failed to create filter: %v", err)
	}

	tests := []struct {
		ip      string
		allowed bool
	}{
		{"192.168.1.50", false},
		{"192.168.1.51", true},
		{"172.16.1.1", false},
		{"172.31.255.255", false},
		{"172.32.0.1", true},
		{"8.8.8.8", true},
	}

	for _, tc := range tests {
		got := filter.IsAllowed(tc.ip)
		if got != tc.allowed {
			t.Errorf("IsAllowed(%q) = %v, expected %v", tc.ip, got, tc.allowed)
		}
	}
}

func TestIPFilter_Precedence_DenylistOverridesAllowlist(t *testing.T) {
	// 10.0.0.0/8 allowed, but 10.0.0.5 blocked explicitly
	filter, err := NewIPFilter([]string{"10.0.0.0/8"}, []string{"10.0.0.5"})
	if err != nil {
		t.Fatalf("failed to create filter: %v", err)
	}

	if !filter.IsAllowed("10.0.0.1") {
		t.Errorf("expected 10.0.0.1 to be allowed")
	}
	if filter.IsAllowed("10.0.0.5") {
		t.Errorf("expected 10.0.0.5 to be blocked by denylist")
	}
}

func TestIPFilter_Middleware(t *testing.T) {
	filter, err := NewIPFilter([]string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("failed to create filter: %v", err)
	}

	handlerCalled := false
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	wrapped := filter.Middleware(nextHandler)

	// 1. Allowed request
	reqAllowed := httptest.NewRequest(http.MethodGet, "/test", nil)
	reqAllowed.RemoteAddr = "127.0.0.1:12345"
	rrAllowed := httptest.NewRecorder()
	wrapped.ServeHTTP(rrAllowed, reqAllowed)

	if rrAllowed.Code != http.StatusOK {
		t.Errorf("expected 200 OK for allowed IP, got %d", rrAllowed.Code)
	}
	if !handlerCalled {
		t.Errorf("expected inner handler to be called")
	}

	// 2. Blocked request
	handlerCalled = false
	reqBlocked := httptest.NewRequest(http.MethodGet, "/test", nil)
	reqBlocked.RemoteAddr = "192.168.1.100:12345"
	rrBlocked := httptest.NewRecorder()
	wrapped.ServeHTTP(rrBlocked, reqBlocked)

	if rrBlocked.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for blocked IP, got %d", rrBlocked.Code)
	}
	if handlerCalled {
		t.Errorf("expected inner handler to NOT be called for blocked IP")
	}
}
