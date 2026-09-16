package security

import (
	"testing"
)

func TestPIIRedactor_Email(t *testing.T) {
	text := "Contact user at john.doe@example.com or support@company.org for assistance."
	redacted := RedactPII(text)

	expected := "Contact user at [REDACTED_EMAIL] or [REDACTED_EMAIL] for assistance."
	if redacted != expected {
		t.Errorf("expected %q, got %q", expected, redacted)
	}
}

func TestPIIRedactor_SSN(t *testing.T) {
	text := "Customer SSN is 123-45-6789, please keep confidential."
	redacted := RedactPII(text)

	expected := "Customer SSN is [REDACTED_SSN], please keep confidential."
	if redacted != expected {
		t.Errorf("expected %q, got %q", expected, redacted)
	}
}

func TestPIIRedactor_Phone(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Call me at (555) 234-5678.", "Call me at [REDACTED_PHONE]."},
		{"Direct line: 555-234-5678.", "Direct line: [REDACTED_PHONE]."},
		{"International: +1 555 234 5678.", "International: [REDACTED_PHONE]."},
	}

	for _, tc := range tests {
		got := RedactPII(tc.input)
		if got != tc.expected {
			t.Errorf("for input %q, expected %q, got %q", tc.input, tc.expected, got)
		}
	}
}

func TestPIIRedactor_CreditCard(t *testing.T) {
	// 4532 0150 1234 5678 with checksum adjusted:
	// A known valid Luhn test card: 49927398716 is 11 digits.
	// 79927398713 is a 11-digit valid Luhn (RFC 7807/ISO 7812).
	// Let's use a 16-digit valid Luhn number:
	// 4000 0012 3456 7899:
	// Let's verify:
	cardValid := "4000-0012-3456-7899"
	digits := extractDigits(cardValid)
	if !isValidLuhn(digits) {
		// Calculate the check digit to make it exactly valid
		sum := 0
		alt := true
		for i := len(digits) - 2; i >= 0; i-- {
			n := int(digits[i] - '0')
			if alt {
				n *= 2
				if n > 9 {
					n -= 9
				}
			}
			sum += n
			alt = !alt
		}
		checkDigit := (10 - (sum % 10)) % 10
		cardValid = digits[:len(digits)-1] + string(rune('0'+checkDigit))
	}

	text := "Charge my card " + cardValid + " for the order."
	redacted := RedactPII(text)

	expected := "Charge my card [REDACTED_CC] for the order."
	if redacted != expected {
		t.Errorf("expected %q, got %q", expected, redacted)
	}

	// Non-Luhn candidate should NOT be redacted
	nonLuhn := "Item SKU: 1234-5678-9012-3456"
	if RedactPII(nonLuhn) != nonLuhn {
		t.Errorf("non-Luhn number was incorrectly redacted: %q", RedactPII(nonLuhn))
	}
}

func TestPIIRedactor_SelectiveConfig(t *testing.T) {
	cfg := PIIRedactorConfig{
		RedactEmails: true,
		RedactPhones: false,
		RedactSSNs:   false,
		RedactCards:  false,
	}
	r := NewPIIRedactor(cfg)

	input := "Email test@example.com, Phone (555) 234-5678, SSN 123-45-6789"
	got := r.Redact(input)
	expected := "Email [REDACTED_EMAIL], Phone (555) 234-5678, SSN 123-45-6789"

	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
