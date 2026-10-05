package metamodel

import (
	"encoding/json"
	"fmt"
)

// FieldsMode decides what a write does with the fields it does not name.
type FieldsMode string

// The two field modes.
const (
	// FieldsReplace writes the map the item carries and nothing else, so a
	// field the item does not name is cleared. The default: it is what
	// every write on this surface did before there was a choice.
	FieldsReplace FieldsMode = "replace"
	// FieldsMerge lays the fields the item names over the ones the row
	// already carries. An explicit null clears one.
	FieldsMerge FieldsMode = "merge"
)

// CheckFieldsMode refuses a mode this surface does not have. Anything
// unrecognised is invalid_input rather than read as replace, because
// reading "mrege" as the destructive default is the one mistake this
// argument exists to prevent.
func CheckFieldsMode(mode FieldsMode) error {
	switch mode {
	case FieldsReplace, FieldsMerge, "":
		return nil
	default:
		return &ValidationError{Code: codeInvalidInput, Fields: []FieldError{{
			Path:    "fields_mode",
			Message: fmt.Sprintf("must be %q or %q", FieldsReplace, FieldsMerge),
		}}}
	}
}

// mergedFields lays the fields an item names over the ones a row already
// carries. A null clears a field, which is the only way to remove one
// once omitting it no longer does.
func mergedFields(stored []byte, incoming map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &out); err != nil {
			return nil, fmt.Errorf("stored fields: %w", err)
		}
	}
	for key, value := range incoming {
		if value == nil {
			delete(out, key)
			continue
		}
		out[key] = value
	}
	return out, nil
}
