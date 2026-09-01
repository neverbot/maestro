package metamodel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
//
// HasDefault is not part of the wire format. On the wire a default is
// declared by the presence of the "default" key and by nothing else, so
// there is exactly one source of truth for the fact; UnmarshalJSON sets
// HasDefault from that presence and MarshalJSON writes the key back only
// when HasDefault is set. See Field.UnmarshalJSON.
type Field struct {
	// Key identifies the field inside the row's jsonb object. It must be
	// lower_snake_case ASCII, at most maxKeyLen characters; see keyPattern
	// for why the rule is that narrow.
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
	// this field replaces.
	HasDefault bool     `json:"-"`
	Default    any      `json:"-"`
	Options    []string `json:"options,omitempty"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
}

// fieldJSON is Field's wire shape. Default is a *json.RawMessage so the
// decoder can tell "the key was absent" from "the key was present and held
// false, 0 or an empty string" — the distinction a plain `any` destroys.
type fieldJSON struct {
	Key      string           `json:"key"`
	Label    string           `json:"label,omitempty"`
	Type     FieldType        `json:"type"`
	Required bool             `json:"required,omitempty"`
	Default  *json.RawMessage `json:"default,omitempty"`
	Options  []string         `json:"options,omitempty"`
	Min      *float64         `json:"min,omitempty"`
	Max      *float64         `json:"max,omitempty"`
}

// UnmarshalJSON decodes a field, declaring a default when — and only when —
// the "default" key is present and not null.
//
// This is the path every agent-authored schema takes: MCP hands Task 3 a
// field_schema straight off the wire, so a default that is not inferred here
// is a default that is silently dropped. An explicit null is not a default:
// null is how this package spells "not set" everywhere else, and Validate
// already treats a null value as an absent one.
func (f *Field) UnmarshalJSON(raw []byte) error {
	var w fieldJSON
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	*f = Field{
		Key:      w.Key,
		Label:    w.Label,
		Type:     w.Type,
		Required: w.Required,
		Options:  w.Options,
		Min:      w.Min,
		Max:      w.Max,
	}
	if w.Default != nil && string(*w.Default) != "null" {
		if err := json.Unmarshal(*w.Default, &f.Default); err != nil {
			return err
		}
		f.HasDefault = true
	}
	return nil
}

// MarshalJSON writes the "default" key only for a field that declares one,
// so the value round trips back through UnmarshalJSON to the same
// HasDefault. A field with HasDefault set and a nil Default cannot be
// spelled on the wire; Check rejects it, since nil is not a value any field
// type could hold.
func (f Field) MarshalJSON() ([]byte, error) {
	w := fieldJSON{
		Key:      f.Key,
		Label:    f.Label,
		Type:     f.Type,
		Required: f.Required,
		Options:  f.Options,
		Min:      f.Min,
		Max:      f.Max,
	}
	if f.HasDefault && f.Default != nil {
		encoded, err := json.Marshal(f.Default)
		if err != nil {
			return nil, err
		}
		w.Default = (*json.RawMessage)(&encoded)
	}
	return json.Marshal(w)
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

// maxKeyLen caps a field key. It is generous for a human-readable
// identifier and short enough that a key can be used verbatim wherever
// Maestro later needs one — a jsonb key, a view-query token, a flattened
// column name.
const maxKeyLen = 64

// keyPattern is the field-key rule: lower_snake_case ASCII, starting with a
// letter. It is deliberately narrow, and every exclusion pays for itself:
//
//   - No dot, because "fields.<key>" is the error path this package returns;
//     a key containing a dot makes "fields.a.b" ambiguous between the field
//     "a.b" and a nested "b" inside "a".
//   - No upper case, so two keys can never differ only by case — a collision
//     no case-sensitive map would catch but every human reader would trip
//     over. Lowercasing by rule beats detecting the collision after the fact.
//   - No whitespace, brackets or dashes, so a key is a single token wherever
//     it is later quoted, flattened or parsed.
//   - ASCII only, so no two spellings of one key (NFC and NFD forms of the
//     same accented word) can coexist as different keys.
//
// The rule is enforced at declaration time, where an agent can still act on
// the news, rather than being discovered six hundred rows later.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Check validates the schema itself, before anything is stored against it.
// Every problem in the whole schema is reported in one pass, including
// several problems on one field: an agent authoring a schema should be able
// to fix all of it at once.
func (s Schema) Check() error {
	seen := make(map[string]struct{}, len(s))
	var problems []FieldError

	for i, f := range s {
		path := fmt.Sprintf("field_schema[%d]", i)
		problem := func(msg string) {
			problems = append(problems, FieldError{Path: path, Message: msg})
		}

		// A bad key never short-circuits the rest of the field: it would hide
		// problems the same agent has to fix in the same edit.
		switch {
		case f.Key == "":
			problem("key is required")
		case len(f.Key) > maxKeyLen:
			problem(fmt.Sprintf("key must be at most %d characters", maxKeyLen))
		case !keyPattern.MatchString(f.Key):
			problem("key must be lower_snake_case: a letter, then letters, digits or underscores")
		default:
			if _, dup := seen[f.Key]; dup {
				problem("duplicate key " + f.Key)
			}
			seen[f.Key] = struct{}{}
		}

		typeOK := false
		switch f.Type {
		case "":
			problem("type is required")
		case FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText:
			typeOK = true
		case FieldEnum:
			if len(f.Options) == 0 {
				problem("an enum field needs options")
			} else {
				typeOK = true
			}
		default:
			problem("unknown type " + string(f.Type))
		}

		// Options and bounds are per-type facilities. Declaring one on a type
		// that cannot use it is never what the author meant, and silence here
		// reads to them as acceptance.
		if f.Type != FieldEnum && len(f.Options) > 0 {
			problem("options apply only to an enum field")
		}
		if f.Type == FieldEnum {
			seenOpt := make(map[string]struct{}, len(f.Options))
			for j, opt := range f.Options {
				if strings.TrimSpace(opt) == "" {
					problem(fmt.Sprintf("option %d is empty", j))
					continue
				}
				if _, dup := seenOpt[opt]; dup {
					problem(fmt.Sprintf("duplicate option %q", opt))
				}
				seenOpt[opt] = struct{}{}
			}
		}
		if f.Type != FieldNumber && (f.Min != nil || f.Max != nil) {
			problem("min and max apply only to a number field")
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			problem(fmt.Sprintf("min %v is above max %v, so no value is legal", *f.Min, *f.Max))
		}

		// Required and a default are mutually exclusive. Validate applies the
		// default before it can ever complain that the field is missing, so
		// the pair makes Required unreachable; rejecting it here says so at
		// the only moment the author can still choose which one they meant.
		if f.Required && f.HasDefault {
			problem("a required field cannot also declare a default: the default would always win")
		}

		// A declared default must itself be a value the field could actually
		// hold — same coercion and bounds logic Validate applies to a real
		// value, so a schema can never declare a default no direct write
		// could ever produce (Default: "yes" on a bool field, Default: 200
		// on a number field whose Max is 70, and so on).
		if typeOK && f.HasDefault {
			if _, err := coerce(f, f.Default); err != nil {
				problem("default: " + err.Error())
			}
		}
	}

	if len(problems) > 0 {
		return &SchemaError{Fields: problems}
	}
	return nil
}
