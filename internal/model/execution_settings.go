package model

// Unset loop counts and time limits stay zero and mean unlimited. File size and
// per-response output size still use their application defaults at runtime;
// these defaults are never written back into workspace settings.
const (
	DefaultConcurrency     = 2
	MaxConcurrency         = 10
	DefaultMaxOutputTokens = 8192
	DefaultMaxFileBytes    = 524288
)

// EffectiveConcurrency returns the runtime worker count. Zero preserves the
// user's unspecified setting while selecting the application default.
func (c Config) EffectiveConcurrency() int {
	if c.Concurrency == 0 {
		return DefaultConcurrency
	}
	return c.Concurrency
}

func (c Config) EffectiveMaxAttempts() int {
	return c.MaxAttempts
}

func (c Config) EffectiveMaxTurns() int {
	return c.MaxTurns
}

func (c Config) EffectiveMaxOutputTokens() int {
	if c.MaxOutputTokens == 0 {
		return DefaultMaxOutputTokens
	}
	return c.MaxOutputTokens
}

func (c Config) EffectiveMaxFileBytes() int {
	if c.MaxFileBytes == 0 {
		return DefaultMaxFileBytes
	}
	return c.MaxFileBytes
}

func (c Config) EffectiveTimeoutSeconds() int {
	return c.TimeoutSeconds
}

// EffectiveExecutionConfig returns a runtime-only copy. The caller's raw
// workspace settings and financial settings are unchanged.
func EffectiveExecutionConfig(c Config) Config {
	c.Concurrency = c.EffectiveConcurrency()
	c.MaxAttempts = c.EffectiveMaxAttempts()
	c.MaxTurns = c.EffectiveMaxTurns()
	c.MaxOutputTokens = c.EffectiveMaxOutputTokens()
	c.MaxFileBytes = c.EffectiveMaxFileBytes()
	c.TimeoutSeconds = c.EffectiveTimeoutSeconds()
	return c
}
