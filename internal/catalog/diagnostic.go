package catalog

import "fmt"

// DiagnosticError identifies the invalid catalog input without requiring a UI
// to parse a translated error message. The underlying error remains available.
type DiagnosticError struct {
	RuleID  string
	Section string
	Err     error
}

func (e *DiagnosticError) Error() string { return e.Err.Error() }
func (e *DiagnosticError) Unwrap() error { return e.Err }

func ruleError(id string, err error) error {
	return &DiagnosticError{RuleID: id, Err: fmt.Errorf("rule %s: %w", id, err)}
}

func filteringError(err error) error {
	return &DiagnosticError{Section: "filtering", Err: err}
}
