package metamodel

import (
	"errors"
	"strings"
	"testing"
)

func questSchema() Schema {
	return Schema{
		{Key: "min_level", Label: "Minimum level", Type: FieldNumber, Required: true, Min: ptrFloat(1), Max: ptrFloat(70)},
		{Key: "summary", Label: "Summary", Type: FieldLongText},
		{Key: "repeatable", Label: "Repeatable", Type: FieldBool, Default: false},
		{Key: "difficulty", Label: "Difficulty", Type: FieldEnum, Options: []string{"trivial", "normal", "elite"}},
		{Key: "tags", Label: "Tags", Type: FieldListText},
	}
}

func ptrFloat(v float64) *float64 { return &v }

func TestValidateAcceptsAWellFormedRow(t *testing.T) {
	out, err := questSchema().Validate(map[string]any{
		"min_level":  float64(20),
		"summary":    "Kill twelve boars.",
		"difficulty": "normal",
		"tags":       []any{"starter", "kill"},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["min_level"] != float64(20) {
		t.Fatalf("min_level = %v", out["min_level"])
	}
	// An absent optional field stays absent: it is never zero-filled.
	if _, present := out["repeatable"]; present {
		t.Fatal("an omitted optional field must not appear in the output")
	}
}

func TestValidateRejectsUnknownField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(1), "min_lvl": float64(3)})
	if err == nil {
		t.Fatal("an unknown field must be an error, never silently dropped")
	}
	if !strings.Contains(err.Error(), "min_lvl") {
		t.Fatalf("the error must name the offending field, got %q", err)
	}
}

func TestValidateRejectsMissingRequiredField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"summary": "no level given"})
	if err == nil {
		t.Fatal("a missing required field must be an error")
	}
	if !strings.Contains(err.Error(), "min_level") {
		t.Fatalf("error = %q, want it to name min_level", err)
	}
}

func TestValidateRejectsWrongType(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": "veinte"})
	if err == nil {
		t.Fatal("a string in a number field must be an error")
	}
	if !strings.Contains(err.Error(), "fields.min_level") {
		t.Fatalf("error = %q, want the field path", err)
	}
	// Assert on the type complaint, not merely on "some error": min_level also
	// carries a minimum, and a validator that skipped the type check entirely
	// would still reject "veinte" for being below it.
	if !strings.Contains(err.Error(), "expected number") {
		t.Fatalf("error = %q, want it to report the type mismatch", err)
	}
}

func TestValidateEnforcesRange(t *testing.T) {
	if _, err := questSchema().Validate(map[string]any{"min_level": float64(999)}); err == nil {
		t.Fatal("a number above max must be an error")
	}
	if _, err := questSchema().Validate(map[string]any{"min_level": float64(0)}); err == nil {
		t.Fatal("a number below min must be an error")
	}
}

func TestValidateEnforcesEnumOptions(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "difficulty": "impossible"})
	if err == nil {
		t.Fatal("a value outside the enum options must be an error")
	}
}

func TestValidateChecksListElements(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "tags": []any{"ok", 3}})
	if err == nil {
		t.Fatal("a non-text element in a list<text> must be an error")
	}
}

func TestValidateAppliesDefaults(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, Default: true}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["repeatable"] != true {
		t.Fatalf("repeatable = %v, want the declared default", out["repeatable"])
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"difficulty": "impossible", "nonsense": 1})
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"min_level", "difficulty", "nonsense"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q; a seeding agent needs every problem in one pass", msg, want)
		}
	}
}

func TestSchemaRejectsDuplicateKeys(t *testing.T) {
	schema := Schema{{Key: "a", Type: FieldText}, {Key: "a", Type: FieldNumber}}
	if err := schema.Check(); err == nil {
		t.Fatal("a schema with duplicate keys must be rejected")
	}
}

func TestSchemaRejectsEnumWithoutOptions(t *testing.T) {
	schema := Schema{{Key: "difficulty", Type: FieldEnum}}
	if err := schema.Check(); err == nil {
		t.Fatal("an enum field with no options must be rejected")
	}
}

func TestSchemaRejectsMissingType(t *testing.T) {
	schema := Schema{{Key: "difficulty"}}
	err := schema.Check()
	if err == nil {
		t.Fatal("a field with no declared type must be rejected")
	}
	if !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("error = %q, want it to say the type is missing", err)
	}
}

func TestSchemaRejectsUnknownType(t *testing.T) {
	schema := Schema{{Key: "colour", Type: FieldType("rgb")}}
	err := schema.Check()
	if err == nil {
		t.Fatal("a field with an unknown type must be rejected")
	}
	if !strings.Contains(err.Error(), "unknown type rgb") {
		t.Fatalf("error = %q, want it to name the unknown type", err)
	}
}

func TestSchemaRejectsEmptyKey(t *testing.T) {
	schema := Schema{{Type: FieldText}}
	err := schema.Check()
	if err == nil {
		t.Fatal("a field with no key must be rejected")
	}
	if !strings.Contains(err.Error(), "key is required") {
		t.Fatalf("error = %q, want it to say the key is missing", err)
	}
}

// A schema that never went through Check can still reach Validate. An unknown
// type and a missing type must each fail loudly there too, and say different
// things, rather than falling through the switch and storing the value.
func TestValidateRejectsUnknownFieldType(t *testing.T) {
	schema := Schema{{Key: "colour", Type: FieldType("rgb")}}
	_, err := schema.Validate(map[string]any{"colour": "red"})
	if err == nil {
		t.Fatal("a value for a field of unknown type must be an error")
	}
	if !strings.Contains(err.Error(), `unknown field type "rgb"`) {
		t.Fatalf("error = %q, want it to name the unknown type", err)
	}
}

func TestValidateRejectsFieldWithNoType(t *testing.T) {
	schema := Schema{{Key: "colour"}}
	_, err := schema.Validate(map[string]any{"colour": "red"})
	if err == nil {
		t.Fatal("a value for a field with no declared type must be an error")
	}
	if !strings.Contains(err.Error(), "declares no type") {
		t.Fatalf("error = %q, want a message distinct from the unknown-type one", err)
	}
}

func TestValidateTreatsNullAsAbsent(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": nil})
	if err == nil {
		t.Fatal("an explicit null in a required field must be an error")
	}
	if !strings.Contains(err.Error(), "fields.min_level: is required") {
		t.Fatalf("error = %q, want the required-field message", err)
	}
}

func TestValidateIgnoresAZeroValuedDefault(t *testing.T) {
	// Field.Default is tagged omitempty, so a false default cannot survive
	// storage. Validate must behave the same way in memory.
	schema := Schema{{Key: "repeatable", Type: FieldBool, Default: false}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, present := out["repeatable"]; present {
		t.Fatalf("out = %v, want a zero-valued default to be treated as no default", out)
	}
}

func TestValidateAcceptsIntegerNumbers(t *testing.T) {
	out, err := questSchema().Validate(map[string]any{"min_level": 20})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["min_level"] != float64(20) {
		t.Fatalf("min_level = %#v, want a float64", out["min_level"])
	}
}

func TestValidationErrorIsSchemaViolation(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"nope": 1})
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("errors.Is(%v, ErrSchemaViolation) = false", err)
	}
}

func TestVersionConflictErrorIsErrVersionConflict(t *testing.T) {
	err := error(&VersionConflictError{Current: 7})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatal("errors.Is(err, ErrVersionConflict) = false")
	}
	if !strings.Contains(err.Error(), "7") {
		t.Fatalf("error = %q, want it to report the current version", err)
	}
}

func TestSchemaRoundTripsThroughJSON(t *testing.T) {
	raw, err := questSchema().JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	back, err := ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if len(back) != len(questSchema()) {
		t.Fatalf("round trip gave %d fields, want %d", len(back), len(questSchema()))
	}
	if back[0].Key != "min_level" || back[0].Type != FieldNumber ||
		back[0].Min == nil || *back[0].Min != 1 || back[0].Max == nil || *back[0].Max != 70 {
		t.Fatalf("first field did not survive the round trip: %#v", back[0])
	}
	if err := back.Check(); err != nil {
		t.Fatalf("Check after round trip: %v", err)
	}
}

func TestParseSchemaAcceptsEmptyInputAndRejectsGarbage(t *testing.T) {
	s, err := ParseSchema(nil)
	if err != nil || len(s) != 0 {
		t.Fatalf("ParseSchema(nil) = %v, %v; want an empty schema", s, err)
	}
	if _, err := ParseSchema([]byte(`{"not":"a list"}`)); err == nil {
		t.Fatal("a schema that is not a list must fail to parse")
	}
}
