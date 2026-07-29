// Package exitcode maps an error's Kind to a process exit code.
//
// The codes are the CLI's contract with anything scripting it, so they are
// fixed constants rather than an iota sequence: inserting a Kind must never
// renumber an existing code.
//
// Codes 7, 8 and 9 are contiguous on purpose. Rate limited, server error and
// network failure are exactly the failures worth retrying, so:
//
//	7 <= code <= 9 means retry
//
// Everything else means the request will fail the same way again until
// something changes.
package exitcode

import "github.com/geckoboard/bugsnag-cli/internal/apierr"

const (
	OK            = 0
	Internal      = 1
	Usage         = 2
	Config        = 3
	Auth          = 4
	NotFound      = 5
	BadRequest    = 6
	RateLimited   = 7
	Server        = 8
	Network       = 9
	Canceled      = 10
	UntrustedHost = 11
	Decode        = 12

	// RetryableMin and RetryableMax bound the retryable band.
	RetryableMin = RateLimited
	RetryableMax = Network
)

// Of returns the exit code for err. A nil error is OK.
func Of(err error) int {
	if err == nil {
		return OK
	}
	return forKind(apierr.KindOf(err))
}

// forKind maps a Kind to its exit code.
func forKind(k apierr.Kind) int {
	switch k {
	case apierr.KindInternal:
		return Internal
	case apierr.KindUsage:
		return Usage
	case apierr.KindConfig:
		return Config
	case apierr.KindAuth:
		return Auth
	case apierr.KindNotFound:
		return NotFound
	case apierr.KindBadRequest:
		return BadRequest
	case apierr.KindRateLimited:
		return RateLimited
	case apierr.KindServer:
		return Server
	case apierr.KindNetwork:
		return Network
	case apierr.KindCanceled:
		return Canceled
	case apierr.KindUntrustedHost:
		return UntrustedHost
	case apierr.KindDecode:
		return Decode
	}
	// An unmapped Kind is a bug in this tool.
	return Internal
}

// Retryable reports whether an exit code indicates the command is worth
// retrying. This is the definition the documented `7 <= code <= 9` rule refers
// to; nothing should hard-code the bounds.
func Retryable(code int) bool {
	return code >= RetryableMin && code <= RetryableMax
}
