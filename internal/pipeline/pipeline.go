// Package pipeline implements a pluggable pre/post-processing chain for
// LLM requests and responses. This is how enterprise AI gateways implement
// guardrails: PII detection, prompt injection detection, content moderation,
// and output format enforcement.
//
// Processors are registered with a direction (PreProcess or PostProcess)
// and run in order. Any processor can halt the chain by returning an error.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/erasulov/llm-gateway/internal/provider"
)

// Direction indicates when a processor runs.
type Direction int

const (
	PreProcess  Direction = iota // Runs on request before provider call.
	PostProcess                  // Runs on response after provider call.
)

func (d Direction) String() string {
	if d == PreProcess {
		return "pre"
	}
	return "post"
}

// Processor is the interface for pipeline steps.
type Processor interface {
	// Name returns the processor's identifier for logging and metrics.
	Name() string

	// Direction returns when this processor runs (pre or post).
	Direction() Direction

	// Process inspects and optionally modifies the request/response.
	// For PreProcess: resp is nil.
	// For PostProcess: both req and resp are available.
	// Return an error to halt the pipeline and reject the request.
	Process(ctx context.Context, req *provider.ChatRequest, resp *provider.ChatResponse) error
}

// Pipeline manages the ordered chain of pre and post processors.
type Pipeline struct {
	pre  []Processor
	post []Processor
}

// New creates an empty pipeline.
func New() *Pipeline {
	return &Pipeline{}
}

// Register adds a processor to the pipeline.
func (p *Pipeline) Register(proc Processor) {
	switch proc.Direction() {
	case PreProcess:
		p.pre = append(p.pre, proc)
	case PostProcess:
		p.post = append(p.post, proc)
	}
	slog.Info("pipeline: registered processor",
		"name", proc.Name(),
		"direction", proc.Direction().String(),
	)
}

// RunPre executes all pre-processors on the request.
// Returns an error if any processor rejects the request.
func (p *Pipeline) RunPre(ctx context.Context, req *provider.ChatRequest) error {
	for _, proc := range p.pre {
		start := time.Now()
		if err := proc.Process(ctx, req, nil); err != nil {
			slog.Warn("pipeline: pre-processor rejected request",
				"processor", proc.Name(),
				"error", err,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			return fmt.Errorf("pipeline %s: %w", proc.Name(), err)
		}
		slog.Debug("pipeline: pre-processor passed",
			"processor", proc.Name(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
	return nil
}

// RunPost executes all post-processors on the response.
// Returns an error if any processor rejects the response.
func (p *Pipeline) RunPost(ctx context.Context, req *provider.ChatRequest, resp *provider.ChatResponse) error {
	for _, proc := range p.post {
		start := time.Now()
		if err := proc.Process(ctx, req, resp); err != nil {
			slog.Warn("pipeline: post-processor rejected response",
				"processor", proc.Name(),
				"error", err,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			return fmt.Errorf("pipeline %s: %w", proc.Name(), err)
		}
		slog.Debug("pipeline: post-processor passed",
			"processor", proc.Name(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
	return nil
}
