package model

// Worker concurrency is the only execution limit exposed to users. The agent
// continues until completion, a real human-decision blocker, or cancellation.
const (
	DefaultConcurrency = 2
	MaxConcurrency     = 10
)

func (c Config) EffectiveConcurrency() int {
	if c.Concurrency == 0 {
		return DefaultConcurrency
	}
	return c.Concurrency
}

// Pricing is accounting metadata, not an execution stop condition.
func EffectiveExecutionConfig(c Config) Config {
	c.Concurrency = c.EffectiveConcurrency()
	return c
}
