package security

import (
	"regexp"
	"strings"
)

// Severity represents the threat level of a detected prompt injection pattern.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// DetectionMatch represents a specific rule triggered during scanning.
type DetectionMatch struct {
	Rule        string   `json:"rule"`
	Severity    Severity `json:"severity"`
	Description string   `json:"description"`
	MatchedText string   `json:"matched_text,omitempty"`
}

// ScanResult contains the overall security assessment of the scanned prompt.
type ScanResult struct {
	Safe        bool             `json:"safe"`
	RiskScore   float64          `json:"risk_score"`
	Matches     []DetectionMatch `json:"matches,omitempty"`
}

type patternRule struct {
	name        string
	regex       *regexp.Regexp
	severity    Severity
	weight      float64
	description string
}

var guardrailRules = []patternRule{
	// 1. Instruction Override / Reset
	{
		name:        "instruction_override",
		regex:       regexp.MustCompile(`(?i)(ignore|disregard|forget|override)\s+(all\s+)?(previous|prior|above)\s+(instructions|prompts|directives|rules|commands)`),
		severity:    SeverityCritical,
		weight:      0.8,
		description: "Attempted override of prior system instructions",
	},
	{
		name:        "system_prompt_extraction",
		regex:       regexp.MustCompile(`(?i)(reveal|show|display|leak|print|output)\s+(your\s+)?(system\s+prompt|initial\s+instructions|core\s+directives|base\s+prompt)`),
		severity:    SeverityHigh,
		weight:      0.75,
		description: "Attempted extraction of system prompt",
	},
	{
		name:        "repeat_above",
		regex:       regexp.MustCompile(`(?i)repeat\s+(everything|all\s+words|the\s+text)\s+(above|before|prior)`),
		severity:    SeverityMedium,
		weight:      0.4,
		description: "Attempted replay of upstream prompt context",
	},

	// 2. Jailbreak Persona Hijacking
	{
		name:        "dan_jailbreak",
		regex:       regexp.MustCompile(`(?i)\b(dan\s+mode|do\s+anything\s+now|jailbreak(ed)?\s+mode|developer\s+mode\s+(enabled|active|on))\b`),
		severity:    SeverityCritical,
		weight:      0.9,
		description: "Known DAN or developer mode jailbreak persona",
	},
	{
		name:        "safety_bypass",
		regex:       regexp.MustCompile(`(?i)(bypass|disable|ignore)\s+(all\s+)?(content\s+filter|safety\s+filter|guardrails?|ethical\s+guidelines|ai\s+policy)`),
		severity:    SeverityCritical,
		weight:      0.85,
		description: "Explicit attempt to bypass content or safety filters",
	},
	{
		name:        "persona_denial",
		regex:       regexp.MustCompile(`(?i)(you\s+are\s+no\s+longer\s+an?\s+ai|pretend\s+(you\s+have\s+no|there\s+are\s+no)\s+(ethics|rules|filters|limits))`),
		severity:    SeverityHigh,
		weight:      0.7,
		description: "Persona denial and unrestricted capability simulation",
	},

	// 3. Special Token & Delimiter Injection
	{
		name:        "special_token_injection",
		regex:       regexp.MustCompile(`(<\|im_start\|>|<\|im_end\|>|<\|endoftext\|>|\[INST\]|\[/INST\]|<\s*/\s*system\s*>)`),
		severity:    SeverityCritical,
		weight:      0.95,
		description: "Prompt template delimiter and special token injection",
	},
}

// PromptDetector analyzes input text for prompt injections and adversarial patterns.
type PromptDetector struct {
	rules           []patternRule
	blockThreshold  float64
}

// NewPromptDetector creates a detector with default guardrail rules and blocking threshold.
// Default block threshold is 0.7.
func NewPromptDetector(threshold float64) *PromptDetector {
	if threshold <= 0 {
		threshold = 0.7
	}
	return &PromptDetector{
		rules:          guardrailRules,
		blockThreshold: threshold,
	}
}

// Scan analyzes a string for prompt injection patterns and calculates a risk score.
func (d *PromptDetector) Scan(text string) ScanResult {
	if strings.TrimSpace(text) == "" {
		return ScanResult{Safe: true, RiskScore: 0.0}
	}

	var matches []DetectionMatch
	var maxWeight float64
	var totalWeight float64

	for _, rule := range d.rules {
		if loc := rule.regex.FindString(text); loc != "" {
			matches = append(matches, DetectionMatch{
				Rule:        rule.name,
				Severity:    rule.severity,
				Description: rule.description,
				MatchedText: loc,
			})
			if rule.weight > maxWeight {
				maxWeight = rule.weight
			}
			totalWeight += rule.weight
		}
	}

	if len(matches) == 0 {
		return ScanResult{Safe: true, RiskScore: 0.0}
	}

	// Composite score: maximum weight plus diminished return for multiple matches
	riskScore := maxWeight + (totalWeight-maxWeight)*0.15
	if riskScore > 1.0 {
		riskScore = 1.0
	}

	safe := riskScore < d.blockThreshold

	return ScanResult{
		Safe:      safe,
		RiskScore: riskScore,
		Matches:   matches,
	}
}
