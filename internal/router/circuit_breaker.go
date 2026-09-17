package router

import (
	"sync"
	"time"
)

type CircuitState string

const (
	StateClosed   CircuitState = "CLOSED"
	StateHalfOpen CircuitState = "HALF_OPEN"
	StateOpen     CircuitState = "OPEN"
)

type CircuitBreaker struct {
	mu                  sync.RWMutex
	state               CircuitState
	failureThreshold    int
	cooldown            time.Duration
	consecutiveFailures int
	consecutiveSuccesses int
	lastFailureTime     time.Time
	nowFunc             func() time.Time
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		cooldown:         cooldown,
		nowFunc:          time.Now,
	}
}

// Allow reports whether a request is permitted to proceed through the breaker.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.lastFailureTime) >= cb.cooldown {
			cb.state = StateHalfOpen
			cb.consecutiveSuccesses = 0
			return true
		}
		return false
	case StateHalfOpen:
		// Allow single probe request
		return true
	default:
		return true
	}
}

// RecordSuccess marks a successful call through the breaker.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.nowFunc()
	if cb.state == StateOpen && now.Sub(cb.lastFailureTime) >= cb.cooldown {
		cb.state = StateHalfOpen
	}

	if cb.state == StateHalfOpen {
		cb.consecutiveSuccesses++
		if cb.consecutiveSuccesses >= 1 {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
			cb.consecutiveSuccesses = 0
		}
	} else if cb.state == StateClosed {
		cb.consecutiveFailures = 0
	}
}

// RecordFailure marks a failed call and transitions state if threshold exceeded.
func (cb *CircuitBreaker) RecordFailure(err error) {
	if !IsTransientFailure(err) {
		// Non-transient errors (e.g. 400 Bad Request, invalid json) do not trip the circuit breaker
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	cb.lastFailureTime = cb.nowFunc()

	if cb.state == StateHalfOpen {
		cb.state = StateOpen
	} else if cb.consecutiveFailures >= cb.failureThreshold {
		cb.state = StateOpen
	}
}

func (cb *CircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.state == StateOpen && cb.nowFunc().Sub(cb.lastFailureTime) >= cb.cooldown {
		return StateHalfOpen
	}
	return cb.state
}

func (cb *CircuitBreaker) GetConsecutiveFailures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.consecutiveFailures
}

// Reset resets the circuit breaker back to StateClosed and clears failure counts.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.consecutiveFailures = 0
	cb.consecutiveSuccesses = 0
	cb.lastFailureTime = time.Time{}
}

// IsTransientFailure returns true if the error qualifies as a transient failure
// (HTTP 429, HTTP 5xx, network timeouts, connection resets).
func IsTransientFailure(err error) bool {
	return ShouldTripBreaker(err)
}
