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
	Default  any       `json:"default,omitempty"`
	Options  []string  `json:"options,omitempty"`
	Min      *float64  `json:"min,omitempty"`
	Max      *float64  `json:"max,omitempty"`
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

		switch f.Type {
		case "":
			problems = append(problems, FieldError{Path: path, Message: "type is required"})
		case FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText:
		case FieldEnum:
			if len(f.Options) == 0 {
				problems = append(problems, FieldError{Path: path, Message: "an enum field needs options"})
			}
		default:
			problems = append(problems, FieldError{Path: path, Message: "unknown type " + string(f.Type)})
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Fields: problems}
	}
	return nil
}
