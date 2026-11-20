package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// Constraints defines deterministic parameter predicates for standing owner Grants (Spec 8.6).
type Constraints struct {
	Equals    map[string]string   `json:"equals,omitempty"`
	OneOf     map[string][]string `json:"one_of,omitempty"`
	MaxNumber map[string]float64  `json:"max_number,omitempty"`
}

// HasAny reports whether at least one parameter constraint is configured.
func (c Constraints) HasAny() bool {
	return len(c.Equals) > 0 || len(c.OneOf) > 0 || len(c.MaxNumber) > 0
}

// Match checks raw JSON tool arguments against c; returns (false, reason) naming the failing field on mismatch.
func (c Constraints) Match(input json.RawMessage) (bool, string) {
	trimmed := bytes.TrimSpace(input)
	if !c.HasAny() {
		if len(trimmed) == 0 {
			return true, ""
		}
		if trimmed[0] != '{' {
			return false, "tool input must be a JSON object"
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return false, "tool input must be a valid JSON object"
		}
		return true, ""
	}

	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false, "tool input must be a JSON object"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return false, "tool input must be a valid JSON object"
	}

	for _, k := range sortedKeys(c.Equals) {
		rawVal, exists := fields[k]
		if !exists {
			return false, fmt.Sprintf("missing constrained field %q", k)
		}
		var got string
		if err := json.Unmarshal(rawVal, &got); err != nil || got != c.Equals[k] {
			return false, fmt.Sprintf("field %q does not equal %q", k, c.Equals[k])
		}
	}

	for _, k := range sortedKeys(c.OneOf) {
		rawVal, exists := fields[k]
		if !exists {
			return false, fmt.Sprintf("missing constrained field %q", k)
		}
		var got string
		if err := json.Unmarshal(rawVal, &got); err != nil || !slices.Contains(c.OneOf[k], got) {
			return false, fmt.Sprintf("field %q is not in allowed list", k)
		}
	}

	for _, k := range sortedKeys(c.MaxNumber) {
		rawVal, exists := fields[k]
		if !exists {
			return false, fmt.Sprintf("missing constrained field %q", k)
		}
		var got float64
		if err := json.Unmarshal(rawVal, &got); err != nil {
			return false, fmt.Sprintf("field %q must be a number", k)
		}
		if got > c.MaxNumber[k] {
			return false, fmt.Sprintf("field %q exceeds max %.2f", k, c.MaxNumber[k])
		}
	}

	return true, ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
