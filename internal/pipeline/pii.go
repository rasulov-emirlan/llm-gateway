package pipeline

import (
	"context"
	"errors"
	"regexp"

	"github.com/erasulov/llm-gateway/internal/provider"
)

// ErrPIIDetected is returned when PII is found in the request.
var ErrPIIDetected = errors.New("PII detected in request")

// PIIDetector scans messages for common PII patterns (SSN, credit cards,
// email addresses) and rejects requests that contain them. This is a
// regex-based heuristic — production systems would use a dedicated NER model.
type PIIDetector struct {
	patterns []*regexp.Regexp
}

// NewPIIDetector creates a PII detector with common patterns.
func NewPIIDetector() *PIIDetector {
	return &PIIDetector{
		patterns: []*regexp.Regexp{
			// US Social Security Number (XXX-XX-XXXX)
			regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			// Credit card numbers (13-19 digits, optionally grouped)
			regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`),
			// US phone numbers
			regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`),
		},
	}
}

func (d *PIIDetector) Name() string        { return "pii_detector" }
func (d *PIIDetector) Direction() Direction { return PreProcess }

func (d *PIIDetector) Process(_ context.Context, req *provider.ChatRequest, _ *provider.ChatResponse) error {
	for _, msg := range req.Messages {
		for _, p := range d.patterns {
			if p.MatchString(msg.Content) {
				return ErrPIIDetected
			}
		}
	}
	return nil
}
