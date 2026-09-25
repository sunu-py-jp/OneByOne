package agent

import (
	"testing"

	"onebyone/internal/model"
)

func TestProviderNormalizationKeepsUnsetCountsAndTimeUnlimited(t *testing.T) {
	input := model.Config{Provider: "openai", Deployment: "unit-test-model"}
	resolved, err := normalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MaxAttempts != 0 || resolved.MaxTurns != 0 || resolved.MaxOutputTokens != 8192 || resolved.MaxFileBytes != 524288 || resolved.TimeoutSeconds != 0 {
		t.Fatalf("provider transport imposed a loop/time limit or changed size defaults: %+v", resolved)
	}
	if input.MaxAttempts != 0 || input.MaxTurns != 0 || input.MaxOutputTokens != 0 || input.MaxFileBytes != 0 || input.TimeoutSeconds != 0 || resolved.MaxCostUSD != 0 || resolved.InputPricePerMillion != 0 || resolved.CachedInputPricePerMillion != 0 || resolved.OutputPricePerMillion != 0 {
		t.Fatal("runtime normalization changed raw settings or invented pricing")
	}
	input.MaxAttempts, input.MaxTurns, input.MaxOutputTokens, input.MaxFileBytes, input.TimeoutSeconds = 1, 7, 2048, 65536, 120
	resolved, err = normalizeConfig(input)
	if err != nil || resolved.MaxAttempts != 1 || resolved.MaxTurns != 7 || resolved.MaxOutputTokens != 2048 || resolved.MaxFileBytes != 65536 || resolved.TimeoutSeconds != 120 {
		t.Fatalf("provider normalization ignored explicit limits: %+v, %v", resolved, err)
	}
}
