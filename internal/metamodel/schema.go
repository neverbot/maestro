package metamodel

import (
	"encoding/json"
	"fmt"
)

// FieldType is one of the declarative field types a game may use. There is
// deliberately no reference type: a pointer to another entity is a relation,
// which keeps the graph complete and visible to every traversal.
type FieldType string

// The field types.
const (
	FieldText     FieldType = "text"
	FieldLongText FieldType = "longtext"
	FieldNumber   FieldType = "number"
	FieldBool     FieldType = "bool"
	FieldEnum     FieldType = "enum"
	FieldListText FieldType = "list<text>"
)

// Field is one declared field of an entity type or relation type.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label,omitempty"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	// HasDefault reports whether this field declares a default at all,
	// independently of what Default holds. It exists because Default is
	// `any`: its Go zero value is nil, the very same value an undeclared
	// field carries, and false, 0 and "" are ordinary, legitimate defaults
	// a game may want to declare (repeatable: false, starting_credits: 0).
	// A reflect-based "is Default the zero value" rule cannot tell "declared
	// false" from "never declared" apart — that confusion is exactly what
	// this field replaces. json.Marshal on an `any` field's omitempty tag
	// only omits a nil interface, not a non-nil interface holding a zero
	// concrete value, so HasDefault:true/Default:false already survives a
	// jsonb round trip through Schema.JSON/ParseSchema unchanged; see
	// TestSchemaRoundTripPreservesADeclaredZeroValuedDefault.
	HasDefault bool     `json:"has_default,omitempty"`
	Default    any      `json:"default,omitempty"`
	Options    []string `json:"options,omitempty"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
}

// Schema is the ordered list of fields a type declares.
type Schema []Field

// ParseSchema decodes a schema from its stored jsonb representation.
func ParseSchema(raw []byte) (Schema, error) {
	if len(raw) == 0 {
		return Schema{}, nil
	}
	var s Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode field schema: %w", err)
	}
	return s, nil
}

// JSON encodes the schema for storage.
func (s Schema) JSON() ([]byte, error) {
	if s == nil {
		s = Schema{}
	}
	return json.Marshal(s)
}

// Check validates the schema itself, before anything is stored against it.
func (s Schema) Check() error {
	seen := make(map[string]struct{}, len(s))
	var problems []FieldError

	for i, f := range s {
		path := fmt.Sprintf("field_schema[%d]", i)
		if f.Key == "" {
			problems = append(problems, FieldError{Path: path, Message: "key is required"})
			continue
		}
		if _, dup := seen[f.Key]; dup {
			problems = append(problems, FieldError{Path: path, Message: "duplicate key " + f.Key})
		}
		seen[f.Key] = struct{}{}

		typeOK := false
		switch f.Type {
		case "":
			problems = append(problems, FieldError{Path: path, Message: "type is required"})
		case FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText:
			typeOK = true
		case FieldEnum:
			if len(f.Options) == 0 {
				problems = append(problems, FieldError{Path: path, Message: "an enum field needs options"})
			} else {
				typeOK = true
			}
		default:
			problems = append(problems, FieldError{Path: path, Message: "unknown type " + string(f.Type)})
		}

		// A declared default must itself be a value the field could actually
		// hold — same coercion and bounds logic Validate applies to a real
		// value, so a schema can never declare a default no direct write
		// could ever produce (Default: "yes" on a bool field, Default: 200
		// on a number field whose Max is 70, and so on).
		if typeOK && f.HasDefault {
			if _, err := coerce(f, f.Default); err != nil {
				problems = append(problems, FieldError{Path: path, Message: "default: " + err.Error()})
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Fields: problems}
	}
	return nil
}
