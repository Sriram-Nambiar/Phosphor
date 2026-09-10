package router

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/Sriram-Nambiar/Phosphor/internal/provider"
)

// ErrorCategory represents the taxonomy of an upstream or gateway execution error.
type ErrorCategory string

const (
	ErrorCategoryTransient ErrorCategory = "transient"
	ErrorCategoryClient    ErrorCategory = "client"
	ErrorCategoryAuth      ErrorCategory = "auth"
	ErrorCategoryNotFound  ErrorCategory = "not_found"
	ErrorCategoryCanceled  ErrorCategory = "canceled"
	ErrorCategoryUnknown   ErrorCategory = "unknown"
)

// ClassifyError inspects an error and categorizes it into a discrete ErrorCategory.
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return ErrorCategoryUnknown
	}

	// Context cancellation
	if errors.Is(err, context.Canceled) {
		return ErrorCategoryCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorCategoryTransient
	}

	// Upstream HTTP errors
	var httpErr *provider.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 429:
			return ErrorCategoryTransient
		case httpErr.StatusCode >= 500 && httpErr.StatusCode <= 599:
			return ErrorCategoryTransient
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			return ErrorCategoryAuth
		case httpErr.StatusCode == 404:
			return ErrorCategoryNotFound
		case httpErr.StatusCode >= 400 && httpErr.StatusCode <= 499:
			return ErrorCategoryClient
		}
	}

	// Network connection errors, resets, and timeouts
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ErrorCategoryTransient
		}
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrorCategoryTransient
	}

	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		if sysErr == syscall.ECONNRESET || sysErr == syscall.ECONNREFUSED || sysErr == syscall.ETIMEDOUT {
			return ErrorCategoryTransient
		}
	}

	return ErrorCategoryUnknown
}

// ShouldFailFast returns true for client errors (e.g. 400 Bad Request, 422 Unprocessable Entity)
// where retrying alternative providers will produce the same failure.
func ShouldFailFast(err error) bool {
	return ClassifyError(err) == ErrorCategoryClient
}

// ShouldTripBreaker returns true if the error indicates provider degradation
// (rate limits, 5xx server errors, network dropouts).
func ShouldTripBreaker(err error) bool {
	return ClassifyError(err) == ErrorCategoryTransient
}

// IsRetryable returns true if the failure is suitable for failover or retry.
func IsRetryable(err error) bool {
	cat := ClassifyError(err)
	return cat == ErrorCategoryTransient || cat == ErrorCategoryNotFound || cat == ErrorCategoryAuth
}
