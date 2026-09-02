package metamodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// Validate checks a value map against the schema and returns the normalised
// map to store. Every problem is reported at once.
//
// Three rules matter more than the rest, and each exists for a reason:
//   - An unknown field is an error, never dropped: an agent that types
//     "min_lvl" must find out immediately, not six hundred rows later.
//   - An absent optional field with no default stays absent: zero-filling
//     would make "not set" indistinguishable from "set to zero".
//   - A declared default is applied when the field is absent or null, so a
//     schema change gives every new row the value the designer intended.
//     See hasDefault for what counts as declared. The default goes through
//     the same coercion a hand-written value does, so it lands in the row
//     with the same Go type and cannot smuggle past the field's bounds.
func (s Schema) Validate(values map[string]any) (map[string]any, error) {
	byKey := make(map[string]Field, len(s))
	for _, f := range s {
		byKey[f.Key] = f
	}

	var problems []FieldError
	out := make(map[string]any, len(values))

	// Unknown keys are found by ranging a map, whose order Go randomises on
	// every run. They are sorted so the message a caller sees is a function
	// of the input alone: Tasks 3-6 return these strings to agents, and a
	// golden test over them has to be possible.
	var unknown []string
	for key := range values {
		if _, known := byKey[key]; !known {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		problems = append(problems, FieldError{
			Path:    "fields." + key,
			Message: "unknown field for this type",
		})
	}

	for _, f := range s {
		raw, present := values[f.Key]
		path := "fields." + f.Key

		if !present || raw == nil {
			switch {
			case hasDefault(f):
				// The default is coerced exactly like a value written by
				// hand. A schema read back from jsonb never passes through
				// Check again, so without this a schema that was never
				// checked would write an unchecked value into every row it
				// touches, forever.
				value, err := coerce(f, f.Default)
				if err != nil {
					problems = append(problems, FieldError{
						Path:    path,
						Message: "the schema's default is invalid: " + err.Error(),
					})
					continue
				}
				out[f.Key] = value
			case f.Required:
				problems = append(problems, FieldError{Path: path, Message: "is required"})
			}
			continue
		}

		value, err := coerce(f, raw)
		if err != nil {
			problems = append(problems, FieldError{Path: path, Message: err.Error()})
			continue
		}
		out[f.Key] = value
	}

	if len(problems) > 0 {
		return nil, &ValidationError{Fields: problems}
	}
	return out, nil
}

// CheckValues re-validates a stored row against this schema and reports
// whether it still fits, returning nothing a caller could accidentally write
// back.
//
// This is the re-validation path. When a type's field schema is edited,
// every stored row of that type has to be re-examined and flagged, and the
// design is explicit that flagging must not alter the rows: an entity's
// content is the designer's, and a validation pass is not an edit. Validate
// cannot serve that purpose safely — it returns a normalised map with
// declared defaults injected, so a caller who re-validates with it and then
// stores what came back silently back-fills every row it touched. Returning
// only an error removes the possibility rather than documenting against it.
//
// The rules are exactly Validate's, so the two never disagree about whether
// a row is valid; the difference is only what comes back.
func (s Schema) CheckValues(values map[string]any) error {
	_, err := s.Validate(values)
	return err
}

// hasDefault reports whether the field declares a default for Validate to
// apply. Presence, not value, decides it: Field.HasDefault is set
// independently of what Default holds, so a declared default of false, 0 or
// "" — an ordinary thing for a game to declare — is applied exactly like any
// other declared default. See Field.HasDefault's doc comment for why a
// value-based rule (checking whether Default is the zero value) cannot make
// this distinction.
func hasDefault(f Field) bool {
	return f.HasDefault
}

// coerce checks one value against one field declaration.
func coerce(f Field, raw any) (any, error) {
	switch f.Type {
	case "":
		return nil, errors.New("the schema declares no type for this field")

	case FieldText, FieldLongText:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected text, got %T", raw)
		}
		// The same rule descriptors.go's textProblem states for every
		// other piece of caller-supplied prose in this package: valid
		// UTF-8, no control character, except that a longtext value is
		// free-form prose and keeps its newlines and tabs. A text value
		// is single-line, the same as a name or a label.
		if p := textProblem(s, f.Type == FieldLongText); p != "" {
			return nil, errors.New(p)
		}
		return s, nil

	case FieldNumber:
		n, ok := toFloat(raw)
		if !ok {
			return nil, fmt.Errorf("expected number, got %T", raw)
		}
		// NaN compares false against every bound, so without this it would
		// slip past both Min and Max; the infinities pass whenever the
		// matching bound is unset. Neither survives being written to jsonb,
		// and the failure there carries no field path, so it is caught here.
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, errors.New("must be a finite number")
		}
		if f.Min != nil && n < *f.Min {
			return nil, fmt.Errorf("must be at least %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, fmt.Errorf("must be at most %v", *f.Max)
		}
		return n, nil

	case FieldBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("expected true or false, got %T", raw)
		}
		return b, nil

	case FieldEnum:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("expected one of %v, got %T", f.Options, raw)
		}
		for _, opt := range f.Options {
			if opt == s {
				return s, nil
			}
		}
		return nil, fmt.Errorf("%q is not one of %v", s, f.Options)

	case FieldListText:
		// []string is accepted alongside []any: it is what a Go caller
		// writes as the idiomatic literal for a list<text> default (Tasks
		// 3 and 4 both build schemas in code), and every value that
		// reaches this branch is going to be a string regardless — []any
		// is only what a JSON decoder happens to produce.
		if strs, ok := raw.([]string); ok {
			out := make([]any, len(strs))
			for i, s := range strs {
				// A list<text> element is a tag or an alias, rendered as
				// one line the same as a text value — see coerce's
				// FieldText case.
				if p := textProblem(s, false); p != "" {
					return nil, fmt.Errorf("element %d %s", i, p)
				}
				out[i] = s
			}
			return out, nil
		}
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("expected a list of text, got %T", raw)
		}
		out := make([]any, 0, len(list))
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, expected text", i, item)
			}
			if p := textProblem(s, false); p != "" {
				return nil, fmt.Errorf("element %d %s", i, p)
			}
			out = append(out, s)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unknown field type %q", f.Type)
	}
}

// toFloat widens every numeric shape that can reach the validator to
// float64. That is more than encoding/json's default decode produces: a
// decoder configured with UseNumber yields json.Number, which the MCP SDK is
// free to do, and Go callers inside Maestro pass the small and unsigned
// widths. Missing any of them would break every number field at once, and
// widening is far cheaper than finding out which decoder is in play.
//
// Finiteness is deliberately not decided here: NaN and the infinities widen
// cleanly, and coerce rejects them with a message that says what is actually
// wrong rather than claiming the value is not a number.
func toFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
