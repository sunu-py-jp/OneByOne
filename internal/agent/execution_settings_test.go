package agent

import (
	"encoding/json"
	"onebyone/internal/model"
	"testing"
)

func TestProviderNormalizationExposesNoRuntimeStopSettings(t *testing.T) {
	input := model.Config{Provider: "openai", Deployment: "unit-test-model", InputPricePerMillion: 1}
	resolved, err := normalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Provider != resolved.Provider || resolved.InputPricePerMillion != 1 {
		t.Fatal("normalization lost provider or accounting metadata")
	}
	data, _ := json.Marshal(resolved)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for _, key := range []string{"maxAttempts", "maxTurns", "maxOutputTokens", "maxFileBytes", "timeoutSeconds", "maxCostUSD"} {
		if _, ok := fields[key]; ok {
			t.Errorf("retired setting exposed: %s", key)
		}
	}
}
