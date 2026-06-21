package workflow

import (

	"github.com/anatolykoptev/go-kit/ratelimit"
)

// rateLimitRegistry holds per-provider QPS limiters.
// Populated via WithRateLimit engine option; empty = unlimited (default).
type rateLimitRegistry struct {
	limiters map[string]*ratelimit.KeyLimiter // provider → limiter
}

// newRateLimitRegistry creates an empty registry (unlimited by default).
func newRateLimitRegistry() *rateLimitRegistry {
	return &rateLimitRegistry{limiters: make(map[string]*ratelimit.KeyLimiter)}
}

// add registers a QPS limiter for the given provider name.
// rate is tokens per second, burst is the bucket size.
func (r *rateLimitRegistry) add(provider string, rate float64, burst int) {
	r.limiters[provider] = ratelimit.NewKeyLimiter(rate, burst)
}

// check returns nil when the call is within rate, or a transient error when it
// is throttled. provider is the key (e.g. tool name, agent ID, LLM model).
// An unknown provider (not configured) is always allowed.
func (r *rateLimitRegistry) check(provider string) error {
	l, ok := r.limiters[provider]
	if !ok {
		return nil // not configured → unlimited
	}
	if !l.Allow(provider) {
		return &rateLimitExceededError{provider: provider}
	}
	return nil
}

// rateLimitExceededError is returned when a per-provider QPS limit is hit.
// Classified as transient so the engine's retry machinery backs off and retries.
type rateLimitExceededError struct {
	provider string
}

func (e *rateLimitExceededError) Error() string {
	return "rate limit exceeded for " + e.provider
}

// WithRateLimit registers a token-bucket QPS limiter for the named provider/tool.
// rate is the sustained token rate (tokens per second); burst is the bucket size.
// Calls that would exceed the limit return a transient error so existing step
// retry machinery can back off and retry. Default is unlimited (no WithRateLimit).
//
// Example:
//
//	engine := NewEngine(store,
//	    WithRateLimit("claude-3-5-sonnet-20241022", 5, 10),
//	    WithRateLimit("my-tool", 2, 4),
//	)
func WithRateLimit(provider string, rate float64, burst int) EngineOption {
	return func(e *Engine) {
		if e.rateLimits == nil {
			e.rateLimits = newRateLimitRegistry()
		}
		e.rateLimits.add(provider, rate, burst)
	}
}

// stepProviderKey extracts the rate-limit key for a step.
// The key matches the string you pass to WithRateLimit.
// Priority: tool name > model name > agent step id.
// For step kinds not covered here, returns the step kind string.
func stepProviderKey(step *Step) string {
	switch step.Kind {
	case StepTool:
		if name, ok := step.Config["tool"].(string); ok && name != "" {
			return name
		}
	case StepLLM, StepVision:
		if model, ok := step.Config["model"].(string); ok && model != "" {
			return model
		}
	case StepAgent:
		// Agent steps are keyed by step.ID (matches the WithRateLimit provider key
		// for agent steps; the breaker namespace is separate and uses "agent:"+step.ID).
		return step.ID
	case StepA2A:
		if id, ok := step.Config["agent_id"].(string); ok && id != "" {
			return id
		}
	}
	return string(step.Kind)
}
