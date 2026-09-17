package security

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// IPFilter validates and filters incoming HTTP requests based on client IP addresses and CIDR subnets.
type IPFilter struct {
	allowedExact map[string]bool
	allowedNets  []*net.IPNet
	blockedExact map[string]bool
	blockedNets  []*net.IPNet
	hasAllowlist bool
	hasDenylist  bool
}

// NewIPFilter parses and builds an IPFilter with optional allowlist and denylist.
func NewIPFilter(allowed []string, blocked []string) (*IPFilter, error) {
	f := &IPFilter{
		allowedExact: make(map[string]bool),
		blockedExact: make(map[string]bool),
	}

	for _, entry := range allowed {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		f.hasAllowlist = true
		if strings.EqualFold(entry, "localhost") {
			f.allowedExact["127.0.0.1"] = true
			f.allowedExact["::1"] = true
			continue
		}
		if strings.Contains(entry, "/") {
			_, ipNet, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid allowlist CIDR '%s': %w", entry, err)
			}
			f.allowedNets = append(f.allowedNets, ipNet)
		} else {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid allowlist IP '%s'", entry)
			}
			f.allowedExact[ip.String()] = true
		}
	}

	for _, entry := range blocked {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		f.hasDenylist = true
		if strings.EqualFold(entry, "localhost") {
			f.blockedExact["127.0.0.1"] = true
			f.blockedExact["::1"] = true
			continue
		}
		if strings.Contains(entry, "/") {
			_, ipNet, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid denylist CIDR '%s': %w", entry, err)
			}
			f.blockedNets = append(f.blockedNets, ipNet)
		} else {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid denylist IP '%s'", entry)
			}
			f.blockedExact[ip.String()] = true
		}
	}

	return f, nil
}

// IsAllowed checks if an IP string is permitted.
// If denylist is configured and IP matches, returns false.
// If allowlist is configured, IP must match allowlist, otherwise returns false.
// If neither is configured, returns true.
func (f *IPFilter) IsAllowed(ipStr string) bool {
	if f == nil {
		return true
	}
	cleaned := cleanIP(ipStr)
	if cleaned == "" {
		cleaned = strings.TrimSpace(ipStr)
	}
	parsed := net.ParseIP(cleaned)
	if parsed == nil {
		// If cannot parse IP and allowlist is enabled, reject; if only denylist, allow
		return !f.hasAllowlist
	}

	normStr := parsed.String()

	// 1. Check Denylist first
	if f.hasDenylist {
		if f.blockedExact[normStr] {
			return false
		}
		for _, ipNet := range f.blockedNets {
			if ipNet.Contains(parsed) {
				return false
			}
		}
	}

	// 2. Check Allowlist
	if f.hasAllowlist {
		if f.allowedExact[normStr] {
			return true
		}
		for _, ipNet := range f.allowedNets {
			if ipNet.Contains(parsed) {
				return true
			}
		}
		return false
	}

	return true
}

// HasRules returns true if any whitelist or blacklist rules are active.
func (f *IPFilter) HasRules() bool {
	if f == nil {
		return false
	}
	return f.hasAllowlist || f.hasDenylist
}

// Middleware creates an HTTP middleware that enforces the IP filtering policy.
func (f *IPFilter) Middleware(next http.Handler) http.Handler {
	if f == nil || (!f.hasAllowlist && !f.hasDenylist) {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := GetClientIP(r)
		if !f.IsAllowed(clientIP) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":{"message":"Access denied: IP %s is not permitted","type":"permission_denied","code":"ip_forbidden"}}`+"\n", clientIP)
			return
		}
		next.ServeHTTP(w, r)
	})
}
