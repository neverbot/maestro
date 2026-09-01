package metamodel

import (
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
