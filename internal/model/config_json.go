package model

import (
	"bytes"
	"encoding/json"
)

// Retired execution controls are discarded, never used as runtime limits or
// written back. Keep strict validation for every other unknown configuration
// field; this is cleanup of removed settings, not support for old schemas.
func (c *Config) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"maxAttempts", "maxTurns", "maxOutputTokens", "maxFileBytes", "timeoutSeconds", "maxCostUSD"} {
		delete(fields, key)
	}
	cleaned, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	type plain Config
	value := plain(*c)
	decoder := json.NewDecoder(bytes.NewReader(cleaned))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*c = Config(value)
	return nil
}
