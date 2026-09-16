package security

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	// SSN regex: Matches standard 9-digit US Social Security Numbers formatted as XXX-XX-XXXX
	ssnRegex = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)

	// Credit card candidate regex (13-19 digits, possibly separated by hyphens or spaces)
	ccRegex = regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`)

	// Phone number regex: North American and international formatted telephone numbers
	phoneRegex = regexp.MustCompile(`(?:\+?1[-.\s]?)?(?:\([2-9]\d{2}\)\s*|[2-9]\d{2}[-.\s]?)[2-9]\d{2}[-.\s]?\d{4}\b`)

	// Email address regex
	emailRegex = regexp.MustCompile(`\b[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}\b`)
)

// PIIRedactorConfig defines which PII categories to redact.
type PIIRedactorConfig struct {
	RedactCards  bool
	RedactSSNs   bool
	RedactPhones bool
	RedactEmails bool
}

// DefaultPIIConfig returns a config with all PII categories enabled.
func DefaultPIIConfig() PIIRedactorConfig {
	return PIIRedactorConfig{
		RedactCards:  true,
		RedactSSNs:   true,
		RedactPhones: true,
		RedactEmails: true,
	}
}

// PIIRedactor scrubs sensitive personally identifiable information from strings.
type PIIRedactor struct {
	cfg PIIRedactorConfig
}

// NewPIIRedactor creates a new redactor with the specified configuration.
func NewPIIRedactor(cfg PIIRedactorConfig) *PIIRedactor {
	return &PIIRedactor{cfg: cfg}
}

// Redact scrubs configured PII types from the given text string.
func (r *PIIRedactor) Redact(text string) string {
	if text == "" {
		return ""
	}

	if r.cfg.RedactEmails {
		text = emailRegex.ReplaceAllString(text, "[REDACTED_EMAIL]")
	}

	if r.cfg.RedactSSNs {
		text = ssnRegex.ReplaceAllString(text, "[REDACTED_SSN]")
	}

	if r.cfg.RedactPhones {
		text = phoneRegex.ReplaceAllString(text, "[REDACTED_PHONE]")
	}

	if r.cfg.RedactCards {
		text = ccRegex.ReplaceAllStringFunc(text, func(candidate string) string {
			digits := extractDigits(candidate)
			if len(digits) >= 13 && len(digits) <= 19 && isValidLuhn(digits) {
				return "[REDACTED_CC]"
			}
			return candidate
		})
	}

	return text
}

// RedactPII scrubs all standard PII (credit cards, SSNs, phone numbers, emails) using default rules.
func RedactPII(text string) string {
	redactor := NewPIIRedactor(DefaultPIIConfig())
	return redactor.Redact(text)
}

// extractDigits extracts only ASCII numeric characters from a string.
func extractDigits(s string) string {
	var sb strings.Builder
	for _, ch := range s {
		if unicode.IsDigit(ch) {
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// isValidLuhn validates credit card numbers using the Luhn checksum algorithm (mod 10).
func isValidLuhn(digits string) bool {
	sum := 0
	alternate := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alternate {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alternate = !alternate
	}
	return sum%10 == 0
}
