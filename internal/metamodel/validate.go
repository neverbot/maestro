package metamodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

	for key := range values {
		if _, known := byKey[key]; !known {
			problems = append(problems, FieldError{
				Path:    "fields." + key,
				Message: "unknown field for this type",
			})
		}
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
