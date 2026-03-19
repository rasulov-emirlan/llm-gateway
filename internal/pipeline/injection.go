package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/erasulov/llm-gateway/internal/provider"
)

// ErrPromptInjection is returned when a prompt injection attempt is detected.
var ErrPromptInjection = errors.New("potential prompt injection detected")

// PromptInjectionDetector scans user messages for common prompt injection
// patterns. This is a heuristic-based approach — production systems would
// use a trained classifier model.
type PromptInjectionDetector struct {
	patterns []string
}

// NewPromptInjectionDetector creates a detector with common injection patterns.
func NewPromptInjectionDetector() *PromptInjectionDetector {
	return &PromptInjectionDetector{
		patterns: []string{
			"ignore previous instructions",
			"ignore all previous",
			"disregard your instructions",
			"forget your instructions",
			"override your instructions",
			"you are now",
			"new instructions:",
			"system prompt:",
			"[system]",
			"<system>",
			"pretend you are",
			"act as if you have no restrictions",
			"jailbreak",
			"do anything now",
			"developer mode",
		},
	}
}

func (d *PromptInjectionDetector) Name() string        { return "injection_detector" }
func (d *PromptInjectionDetector) Direction() Direction { return PreProcess }

func (d *PromptInjectionDetector) Process(_ context.Context, req *provider.ChatRequest, _ *provider.ChatResponse) error {
	for _, msg := range req.Messages {
		if msg.Role != "user" {
			continue
		}
		lower := strings.ToLower(msg.Content)
		for _, pattern := range d.patterns {
			if strings.Contains(lower, pattern) {
				return ErrPromptInjection
			}
		}
	}
	return nil
}
