package metamodel

import (
	"errors"
	"fmt"
	"reflect"
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
//     See hasDefault for what counts as declared.
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
				out[f.Key] = f.Default
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
// apply. A nil Default declares none, and so does a zero value — false, 0 or
// the empty string. That second rule is forced by storage: Field.Default is
// tagged `json:",omitempty"`, so a zero-valued default is dropped when the
// schema is encoded and comes back as nil when it is read again. Honouring it
// in Go would make an in-memory schema behave differently from the very same
// schema loaded from the database.
func hasDefault(f Field) bool {
	if f.Default == nil {
		return false
	}
	return !reflect.ValueOf(f.Default).IsZero()
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

// toFloat accepts every numeric shape JSON decoding can produce.
func toFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
