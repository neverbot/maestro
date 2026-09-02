package metamodel

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func questSchema() Schema {
	return Schema{
		{Key: "min_level", Label: "Minimum level", Type: FieldNumber, Required: true, Min: ptrFloat(1), Max: ptrFloat(70)},
		{Key: "summary", Label: "Summary", Type: FieldLongText},
		{Key: "repeatable", Label: "Repeatable", Type: FieldBool, HasDefault: true, Default: false},
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
	// repeatable declares a default of false, so an omitted field with a
	// declared default is filled in — never left absent, and never dropped
	// for being the zero value of its type.
	if v, present := out["repeatable"]; !present || v != false {
		t.Fatalf("repeatable = %v, present=%v; want the declared default false to be applied", v, present)
	}
}

func TestValidateLeavesAnUndeclaredOptionalFieldAbsent(t *testing.T) {
	// Contrast with the declared-zero-default case above: a field with no
	// default declared at all (HasDefault false) must stay genuinely absent
	// when omitted, never zero-filled.
	schema := Schema{{Key: "repeatable", Type: FieldBool}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, present := out["repeatable"]; present {
		t.Fatal("a field with no declared default must stay absent when omitted")
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
	// Each case asserts the exact message, not merely that something failed.
	// min_level is bounded on both sides, so "some error" is satisfied by a
	// validator that swapped the two comparisons, or that dropped one of
	// them and rejected the value for the other reason.
	for name, tc := range map[string]struct {
		value any
		want  string
	}{
		"far above max":      {float64(999), "fields.min_level: must be at most 70"},
		"just above max":     {float64(70.5), "fields.min_level: must be at most 70"},
		"far below min":      {float64(0), "fields.min_level: must be at least 1"},
		"fractionally below": {float64(0.5), "fields.min_level: must be at least 1"},
		"negative below min": {float64(-3), "fields.min_level: must be at least 1"},
	} {
		_, err := questSchema().Validate(map[string]any{"min_level": tc.value})
		if err == nil {
			t.Fatalf("%s: %v was accepted", name, tc.value)
		}
		if err.Error() != "schema_violation: "+tc.want {
			t.Fatalf("%s: error = %q, want %q", name, err, tc.want)
		}
	}
}

func TestValidateAcceptsTheBoundsThemselves(t *testing.T) {
	// The bounds are inclusive; an off-by-one in either comparison shows up
	// here rather than in production content.
	for _, v := range []float64{1, 35, 70} {
		if _, err := questSchema().Validate(map[string]any{"min_level": v}); err != nil {
			t.Fatalf("min_level %v: Validate: %v", v, err)
		}
	}
}

func TestValidateEnforcesEnumOptions(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "difficulty": "impossible"})
	if err == nil {
		t.Fatal("a value outside the enum options must be an error")
	}
	want := `schema_violation: fields.difficulty: "impossible" is not one of [trivial normal elite]`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestValidateRejectsANonTextValueInAnEnumField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "difficulty": 3})
	if err == nil {
		t.Fatal("a number in an enum field must be an error")
	}
	if !strings.Contains(err.Error(), "fields.difficulty: expected one of [trivial normal elite], got int") {
		t.Fatalf("error = %q, want the enum type message naming the options", err)
	}
}

// L5: enum matching compares exact bytes. Neither case nor Unicode
// normalisation is folded, and that is the intended behaviour: an option is
// a token a game declared, and Maestro does not second-guess a game's
// vocabulary by deciding that two spellings are the same word. Agents get
// the exact string back in the error message, so the fix is mechanical.
func TestValidateMatchesEnumOptionsByExactBytes(t *testing.T) {
	schema := Schema{{Key: "difficulty", Type: FieldEnum, Options: []string{"normal", "élite"}}}

	if _, err := schema.Validate(map[string]any{"difficulty": "Normal"}); err == nil {
		t.Fatal(`"Normal" was accepted for the option "normal"; enum matching does not fold case`)
	} else if !strings.Contains(err.Error(), `"Normal" is not one of`) {
		t.Fatalf("error = %q, want it to quote the value as sent", err)
	}

	// "élite" spelled NFD: e + U+0301, the same word to a reader and a
	// different string to the matcher.
	nfd := "e\u0301lite"
	if _, err := schema.Validate(map[string]any{"difficulty": nfd}); err == nil {
		t.Fatal("an NFD spelling was accepted for an NFC option; enum matching does not normalise")
	}
	if _, err := schema.Validate(map[string]any{"difficulty": "élite"}); err != nil {
		t.Fatalf("the exact NFC option must be accepted: %v", err)
	}
}

func TestValidateChecksListElements(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "tags": []any{"ok", 3}})
	if err == nil {
		t.Fatal("a non-text element in a list<text> must be an error")
	}
	want := "schema_violation: fields.tags: element 1 is int, expected text"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q; the index and the offending type both matter to whoever fixes it", err, want)
	}
}

func TestValidateRejectsANonListInAListField(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(5), "tags": "starter"})
	if err == nil {
		t.Fatal("a bare string in a list<text> field must be an error")
	}
	if !strings.Contains(err.Error(), "fields.tags: expected a list of text, got string") {
		t.Fatalf("error = %q, want the list type message", err)
	}
}

func TestValidateKeepsAWellFormedListAsStrings(t *testing.T) {
	out, err := questSchema().Validate(map[string]any{"min_level": float64(5), "tags": []any{"starter", "kill"}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	list, ok := out["tags"].([]any)
	if !ok || len(list) != 2 || list[0] != "starter" || list[1] != "kill" {
		t.Fatalf("tags = %#v, want the two strings in order", out["tags"])
	}
}

func TestValidateAppliesDefaults(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: true}}
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

func TestValidateAppliesADeclaredZeroValuedDefault(t *testing.T) {
	// HasDefault, not the value of Default, decides whether a default was
	// declared. false is an ordinary thing for a game to declare as a
	// default, and must be applied like any other declared default.
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: false}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v, present := out["repeatable"]; !present || v != false {
		t.Fatalf("out = %v, want the declared false default to be applied", out)
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

func TestSchemaRoundTripPreservesADeclaredZeroValuedDefault(t *testing.T) {
	// This is the path Tasks 3-6 actually use: a schema is encoded to jsonb,
	// stored, read back and decoded before Validate ever sees it. The round
	// trip, not the in-memory struct literal, is what must decide whether a
	// declared false default survives.
	//
	// Both directions are covered, because only one of them is the path an
	// agent's schema takes. Go -> JSON -> Go is below; JSON -> Go, an
	// agent-shaped field_schema arriving over MCP and validated straight
	// away, is first.
	fromWire, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool","default":false}]`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	wireOut, err := fromWire.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v, present := wireOut["repeatable"]; !present || v != false {
		t.Fatalf("out = %v, want the default declared in agent-shaped JSON to be applied", wireOut)
	}

	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: false}}
	raw, err := schema.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	back, err := ParseSchema(raw)
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	out, err := back.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v, present := out["repeatable"]; !present || v != false {
		t.Fatalf("out = %v, want the declared false default to survive the round trip and be applied", out)
	}
}

func TestSchemaRejectsADefaultOfTheWrongType(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: "yes"}}
	err := schema.Check()
	if err == nil {
		t.Fatal("a default of the wrong type must be rejected")
	}
	if !strings.Contains(err.Error(), "field_schema[0]") {
		t.Fatalf("error = %q, want the field_schema path", err)
	}
}

func TestSchemaRejectsADefaultOutsideItsBounds(t *testing.T) {
	schema := Schema{{Key: "min_level", Type: FieldNumber, Max: ptrFloat(70), HasDefault: true, Default: float64(200)}}
	if err := schema.Check(); err == nil {
		t.Fatal("a default above max must be rejected")
	}
}

func TestSchemaRejectsADefaultNotInEnumOptions(t *testing.T) {
	schema := Schema{{Key: "difficulty", Type: FieldEnum, Options: []string{"trivial", "normal"}, HasDefault: true, Default: "impossible"}}
	if err := schema.Check(); err == nil {
		t.Fatal("a default outside the enum options must be rejected")
	}
}

func TestSchemaRejectsANonTextElementInAListTextDefault(t *testing.T) {
	schema := Schema{{Key: "tags", Type: FieldListText, HasDefault: true, Default: []any{"ok", 3}}}
	if err := schema.Check(); err == nil {
		t.Fatal("a non-text element in a list<text> default must be rejected")
	}
}

func TestSchemaAcceptsAWellFormedDefault(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: false}}
	if err := schema.Check(); err != nil {
		t.Fatalf("Check: %v", err)
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

// --- H1: a default declared over JSON must be honoured -------------------

func TestParseSchemaInfersHasDefaultFromThePresenceOfTheDefaultKey(t *testing.T) {
	// This is the exact shape an agent sends over MCP: a "default" key and
	// nothing else. There is no "has_default" sibling on the wire, so the
	// presence of the key — not a second flag — is what declares a default.
	back, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool","default":false}]`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if !back[0].HasDefault {
		t.Fatalf("field = %#v, want HasDefault true because the JSON carries a default key", back[0])
	}
	out, err := back.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v, present := out["repeatable"]; !present || v != false {
		t.Fatalf("out = %v, want the declared false default applied", out)
	}
}

func TestParseSchemaTreatsAnAbsentDefaultKeyAsNoDefault(t *testing.T) {
	back, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool"}]`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if back[0].HasDefault {
		t.Fatalf("field = %#v, want HasDefault false when no default key is present", back[0])
	}
	out, err := back.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, present := out["repeatable"]; present {
		t.Fatal("a field with no declared default must stay absent when omitted")
	}
}

func TestParseSchemaTreatsAnExplicitNullDefaultAsNoDefault(t *testing.T) {
	// null is how this package spells "not set" everywhere else: Validate
	// treats a null value as an absent one. A null default is therefore no
	// default, not a default of nil.
	back, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool","default":null}]`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if back[0].HasDefault {
		t.Fatalf("field = %#v, want HasDefault false for a null default", back[0])
	}
}

func TestParseSchemaRejectsHasDefaultOnTheWire(t *testing.T) {
	// has_default is not an input key. Accepting it silently — even by
	// ignoring it — would give the wire two sources of truth for one fact,
	// which is what caused this bug: the presence of "default" is the only
	// declaration. A stray has_default, which a confused agent may well
	// send since an earlier iteration accepted it, must be rejected
	// outright, not swallowed.
	if _, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool","has_default":true}]`)); err == nil {
		t.Fatal("a has_default key on the wire must be rejected, not ignored")
	}
}

func TestParseSchemaRejectsAnUnknownKeyInAFieldDeclaration(t *testing.T) {
	// Unknown keys must never be silently dropped: that is the original
	// defect the HasDefault correction existed to fix, and it applies to
	// every key, not only has_default. A typo like "defualt" must surface
	// immediately, not lose the declared default in silence.
	if _, err := ParseSchema([]byte(`[{"key":"a","type":"text","nonsense":42,"defualt":"typo"}]`)); err == nil {
		t.Fatal("an unknown key in a field declaration must be rejected")
	}
}

func TestSchemaJSONOmitsHasDefaultAndSpellsTheDefaultOnce(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: false}}
	raw, err := schema.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(raw), "has_default") {
		t.Fatalf("JSON = %s, want no has_default key on the wire", raw)
	}
	if !strings.Contains(string(raw), `"default":false`) {
		t.Fatalf("JSON = %s, want the declared false default spelled out", raw)
	}
}

func TestSchemaJSONOmitsTheDefaultKeyWhenNoDefaultIsDeclared(t *testing.T) {
	schema := Schema{{Key: "repeatable", Type: FieldBool}}
	raw, err := schema.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.Contains(string(raw), "default") {
		t.Fatalf("JSON = %s, want no default key when none is declared", raw)
	}
}

// --- H2: a default is a value, and goes through the same coercion --------

func TestValidateNormalisesAnAppliedDefault(t *testing.T) {
	// A default declared in Go as an int and an explicit int in the row must
	// end up as the same Go type, or two rows of one type carry two shapes of
	// the same field and every reader downstream has to handle both.
	schema := Schema{{Key: "min_level", Type: FieldNumber, HasDefault: true, Default: 20}}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v, ok := out["min_level"].(float64); !ok || v != 20 {
		t.Fatalf("min_level = %#v, want float64(20) exactly as an explicit 20 would give", out["min_level"])
	}
}

func TestValidateRejectsAnUncheckedBadDefault(t *testing.T) {
	// A schema read back from jsonb is never re-Checked, so Validate is the
	// last line of defence: it must not write a default no direct value could
	// ever produce.
	schema := Schema{{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: "yes"}}
	out, err := schema.Validate(map[string]any{})
	if err == nil {
		t.Fatalf("out = %v, want a bad default to be an error rather than stored", out)
	}
	if !strings.Contains(err.Error(), "fields.repeatable") {
		t.Fatalf("error = %q, want the value-level field path", err)
	}
	if !strings.Contains(err.Error(), "expected true or false") {
		t.Fatalf("error = %q, want it to report why the default is not a bool", err)
	}
	if !strings.Contains(err.Error(), "default") {
		t.Fatalf("error = %q, want it to say the offending value is the schema's default", err)
	}
}

// --- M1/L1: numbers -------------------------------------------------------

func TestValidateRejectsNonFiniteNumbers(t *testing.T) {
	// NaN compares false against both a minimum and a maximum, so a bounded
	// field would accept it; +Inf passes whenever there is no maximum. Either
	// one reaches the storage layer as an opaque "json: unsupported value"
	// panic-adjacent failure with no field path, long after the agent that
	// sent it could act on the news.
	for name, value := range map[string]float64{
		"NaN":  math.NaN(),
		"+Inf": math.Inf(1),
		"-Inf": math.Inf(-1),
	} {
		schema := Schema{{Key: "score", Type: FieldNumber}}
		_, err := schema.Validate(map[string]any{"score": value})
		if err == nil {
			t.Fatalf("%s was accepted; a number field must hold a finite number", name)
		}
		if !strings.Contains(err.Error(), "fields.score: must be a finite number") {
			t.Fatalf("%s: error = %q, want the finite-number message at the field path", name, err)
		}
	}
}

func TestSchemaRejectsANonFiniteDefault(t *testing.T) {
	schema := Schema{{Key: "score", Type: FieldNumber, HasDefault: true, Default: math.NaN()}}
	err := schema.Check()
	if err == nil {
		t.Fatal("a NaN default must be rejected")
	}
	if !strings.Contains(err.Error(), "must be a finite number") {
		t.Fatalf("error = %q, want the finite-number message", err)
	}
}

func TestValidateAcceptsEveryNumericShapeADecoderCanProduce(t *testing.T) {
	// json.Number is what Decoder.UseNumber produces, and the MCP SDK is free
	// to decode that way; the small and unsigned integer widths are what Go
	// callers inside Maestro will pass. Every one of them must land as the
	// same float64 a plain decode would give.
	schema := Schema{{Key: "score", Type: FieldNumber}}
	for name, raw := range map[string]any{
		"float64":     float64(7),
		"float32":     float32(7),
		"int":         int(7),
		"int8":        int8(7),
		"int16":       int16(7),
		"int32":       int32(7),
		"int64":       int64(7),
		"uint":        uint(7),
		"uint8":       uint8(7),
		"uint16":      uint16(7),
		"uint32":      uint32(7),
		"uint64":      uint64(7),
		"json.Number": json.Number("7"),
	} {
		out, err := schema.Validate(map[string]any{"score": raw})
		if err != nil {
			t.Fatalf("%s: Validate: %v", name, err)
		}
		if v, ok := out["score"].(float64); !ok || v != 7 {
			t.Fatalf("%s: score = %#v, want float64(7)", name, out["score"])
		}
	}
}

func TestValidateRejectsAJSONNumberThatIsNotANumber(t *testing.T) {
	schema := Schema{{Key: "score", Type: FieldNumber}}
	_, err := schema.Validate(map[string]any{"score": json.Number("veinte")})
	if err == nil {
		t.Fatal("a json.Number holding a non-number must be an error")
	}
	if !strings.Contains(err.Error(), "expected number") {
		t.Fatalf("error = %q, want the type message", err)
	}
}

// --- M2: a schema declaration must itself be meaningful -------------------

// checkProblems runs Check and returns the message, failing if it passed.
func checkProblems(t *testing.T, s Schema) string {
	t.Helper()
	err := s.Check()
	if err == nil {
		t.Fatalf("schema %v was accepted; want it rejected", s)
	}
	return err.Error()
}

func TestSchemaRejectsAKeyThatIsNotAnIdentifier(t *testing.T) {
	// The key rule is lower_snake_case ASCII: see Field.Key's doc comment for
	// why. Each of these is a shape an agent plausibly sends.
	for name, key := range map[string]string{
		"whitespace only": "   ",
		"leading space":   " min_level",
		"trailing space":  "min_level ",
		"inner space":     "min level",
		"a dot":           "min.level",
		"upper case":      "MinLevel",
		"a leading digit": "1st_level",
		"a leading under": "_min_level",
		"a dash":          "min-level",
		"a bracket":       "min[0]",
		"non-ASCII":       "nivel_mínimo",
	} {
		msg := checkProblems(t, Schema{{Key: key, Type: FieldText}})
		if !strings.Contains(msg, "key must be lower_snake_case") {
			t.Fatalf("%s (%q): error = %q, want the key-format message", name, key, msg)
		}
	}
}

func TestSchemaAcceptsAWellFormedKey(t *testing.T) {
	for _, key := range []string{"a", "min_level", "level2", "a_b_c_9"} {
		if err := (Schema{{Key: key, Type: FieldText}}).Check(); err != nil {
			t.Fatalf("key %q: Check: %v", key, err)
		}
	}
}

func TestSchemaRejectsAnOverlongKey(t *testing.T) {
	msg := checkProblems(t, Schema{{Key: strings.Repeat("a", maxKeyLen+1), Type: FieldText}})
	if !strings.Contains(msg, "key must be at most") {
		t.Fatalf("error = %q, want the key-length message", msg)
	}
}

func TestSchemaRejectsAMinimumAboveItsMaximum(t *testing.T) {
	msg := checkProblems(t, Schema{{Key: "min_level", Type: FieldNumber, Min: ptrFloat(70), Max: ptrFloat(1)}})
	if !strings.Contains(msg, "min 70 is above max 1") {
		t.Fatalf("error = %q, want it to name both bounds", msg)
	}
}

func TestSchemaAcceptsAMinimumEqualToItsMaximum(t *testing.T) {
	// A single legal value is a narrow declaration, not a contradictory one.
	schema := Schema{{Key: "min_level", Type: FieldNumber, Min: ptrFloat(7), Max: ptrFloat(7)}}
	if err := schema.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestSchemaRejectsBoundsOnANonNumberField(t *testing.T) {
	for _, ft := range []FieldType{FieldText, FieldLongText, FieldBool, FieldEnum, FieldListText} {
		schema := Schema{{Key: "a", Type: ft, Options: []string{"x"}, Min: ptrFloat(1)}}
		msg := checkProblems(t, schema)
		if !strings.Contains(msg, "min and max apply only to a number field") {
			t.Fatalf("%s: error = %q, want the misplaced-bounds message", ft, msg)
		}
	}
}

func TestSchemaRejectsOptionsOnANonEnumField(t *testing.T) {
	for _, ft := range []FieldType{FieldText, FieldLongText, FieldNumber, FieldBool, FieldListText} {
		msg := checkProblems(t, Schema{{Key: "a", Type: ft, Options: []string{"x"}}})
		if !strings.Contains(msg, "options apply only to an enum field") {
			t.Fatalf("%s: error = %q, want the misplaced-options message", ft, msg)
		}
	}
}

func TestSchemaRejectsDuplicateEnumOptions(t *testing.T) {
	msg := checkProblems(t, Schema{{Key: "difficulty", Type: FieldEnum, Options: []string{"normal", "normal"}}})
	if !strings.Contains(msg, `duplicate option "normal"`) {
		t.Fatalf("error = %q, want it to name the duplicated option", msg)
	}
}

func TestSchemaRejectsAnEmptyEnumOption(t *testing.T) {
	for name, opts := range map[string][]string{
		"empty string":    {"normal", ""},
		"whitespace only": {"normal", "  "},
	} {
		msg := checkProblems(t, Schema{{Key: "difficulty", Type: FieldEnum, Options: opts}})
		if !strings.Contains(msg, "option 1 is empty") {
			t.Fatalf("%s: error = %q, want the empty-option message with its index", name, msg)
		}
	}
}

// --- M3: required and a default are mutually exclusive --------------------

func TestSchemaRejectsARequiredFieldWithADefault(t *testing.T) {
	// The default branch runs before the required branch, so declaring both
	// makes Required dead: no row could ever fail for omitting the field. A
	// field either has a fallback or it does not.
	msg := checkProblems(t, Schema{{Key: "repeatable", Type: FieldBool, Required: true, HasDefault: true, Default: false}})
	if !strings.Contains(msg, "a required field cannot also declare a default") {
		t.Fatalf("error = %q, want the required-plus-default message", msg)
	}
}

// --- M5: one pass reports every problem, including on a bad key -----------

func TestSchemaReportsEveryProblemOnAFieldWithABadKey(t *testing.T) {
	// A bad key must not hide the rest of the field. The package's whole
	// contract is that one call reports everything an agent has to fix.
	msg := checkProblems(t, Schema{{Key: "", Type: FieldType("rgb")}})
	if !strings.Contains(msg, "key is required") {
		t.Fatalf("error = %q, want the missing-key problem", msg)
	}
	if !strings.Contains(msg, "unknown type rgb") {
		t.Fatalf("error = %q, want the unknown-type problem reported in the same pass", msg)
	}
}

func TestSchemaStillRejectsDuplicateKeysWhenAnEarlierFieldIsBroken(t *testing.T) {
	msg := checkProblems(t, Schema{
		{Key: "", Type: FieldText},
		{Key: "a", Type: FieldText},
		{Key: "a", Type: FieldNumber},
	})
	if !strings.Contains(msg, "duplicate key a") {
		t.Fatalf("error = %q, want the duplicate reported alongside the empty key", msg)
	}
}

// --- a schema declaration failure is not a value failure ------------------

func TestSchemaErrorIsInvalidSchemaAndNotASchemaViolation(t *testing.T) {
	err := (Schema{{Key: "difficulty", Type: FieldEnum}}).Check()
	if !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("errors.Is(%v, ErrInvalidSchema) = false", err)
	}
	if errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("errors.Is(%v, ErrSchemaViolation) = true; a bad declaration is not a bad row", err)
	}
	if !strings.HasPrefix(err.Error(), "invalid_schema: ") {
		t.Fatalf("error = %q, want it to lead with the invalid_schema code", err)
	}
}

func TestValidationErrorIsNotAnInvalidSchema(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"nope": 1})
	if errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("errors.Is(%v, ErrInvalidSchema) = true; a bad row is not a bad declaration", err)
	}
}

// --- M4: the order of reported problems is deterministic ------------------

func TestValidateReportsUnknownFieldsInAStableOrder(t *testing.T) {
	// Unknown fields are discovered by ranging a map, whose order Go
	// randomises per run. Tasks 3-6 hand these strings straight back to an
	// agent and will want golden tests, so the order is sorted, not
	// whichever the runtime happened to pick.
	values := map[string]any{"min_level": float64(5), "zulu": 1, "alpha": 2, "mike": 3}
	want := "schema_violation: fields.alpha: unknown field for this type; " +
		"fields.mike: unknown field for this type; " +
		"fields.zulu: unknown field for this type"
	for i := 0; i < 20; i++ {
		_, err := questSchema().Validate(values)
		if err == nil {
			t.Fatal("expected errors")
		}
		if err.Error() != want {
			t.Fatalf("run %d: error = %q, want %q", i, err, want)
		}
	}
}

func TestValidateReportsUnknownFieldsBeforeFieldProblemsInSchemaOrder(t *testing.T) {
	// The whole message is ordered: unknown keys first, sorted, then each
	// declared field in the order the schema declares it.
	_, err := questSchema().Validate(map[string]any{"nonsense": 1, "difficulty": "impossible"})
	if err == nil {
		t.Fatal("expected errors")
	}
	want := "schema_violation: fields.nonsense: unknown field for this type; " +
		"fields.min_level: is required; " +
		`fields.difficulty: "impossible" is not one of [trivial normal elite]`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

// --- text and bool: the coercions with no negative test until now ---------

func TestValidateRejectsANonTextValueInATextField(t *testing.T) {
	// Stringifying whatever arrives is the most dangerous coercion this
	// package could make: a map, a number or a bool would each be stored as
	// plausible-looking prose and nothing downstream would ever know. Each
	// case names the type it sent, so a validator that formatted the value
	// instead of rejecting it fails here.
	for _, ft := range []FieldType{FieldText, FieldLongText} {
		schema := Schema{{Key: "summary", Type: ft}}
		for name, tc := range map[string]struct {
			value any
			want  string
		}{
			"a number":  {float64(3), "fields.summary: expected text, got float64"},
			"an int":    {3, "fields.summary: expected text, got int"},
			"a bool":    {true, "fields.summary: expected text, got bool"},
			"a list":    {[]any{"a"}, "fields.summary: expected text, got []interface {}"},
			"an object": {map[string]any{"a": 1}, "fields.summary: expected text, got map[string]interface {}"},
		} {
			out, err := schema.Validate(map[string]any{"summary": tc.value})
			if err == nil {
				t.Fatalf("%s: %s was accepted and stored as %#v", ft, name, out["summary"])
			}
			if err.Error() != "schema_violation: "+tc.want {
				t.Fatalf("%s/%s: error = %q, want %q", ft, name, err, tc.want)
			}
		}
	}
}

func TestValidateKeepsTextExactly(t *testing.T) {
	schema := Schema{{Key: "summary", Type: FieldText}}
	out, err := schema.Validate(map[string]any{"summary": "  Kill twelve boars.  "})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if out["summary"] != "  Kill twelve boars.  " {
		t.Fatalf("summary = %q, want the string stored verbatim, untrimmed", out["summary"])
	}
}

func TestValidateRejectsANonBoolValueInABoolField(t *testing.T) {
	for name, tc := range map[string]struct {
		value any
		want  string
	}{
		"the string true": {"true", "fields.repeatable: expected true or false, got string"},
		"one":             {float64(1), "fields.repeatable: expected true or false, got float64"},
		"zero":            {0, "fields.repeatable: expected true or false, got int"},
	} {
		schema := Schema{{Key: "repeatable", Type: FieldBool}}
		out, err := schema.Validate(map[string]any{"repeatable": tc.value})
		if err == nil {
			t.Fatalf("%s was accepted and stored as %#v", name, out["repeatable"])
		}
		if err.Error() != "schema_violation: "+tc.want {
			t.Fatalf("%s: error = %q, want %q", name, err, tc.want)
		}
	}
}

func TestValidateRejectsAnUnknownFieldWithTheExactMessage(t *testing.T) {
	_, err := questSchema().Validate(map[string]any{"min_level": float64(1), "min_lvl": float64(3)})
	if err == nil {
		t.Fatal("an unknown field must be an error")
	}
	want := "schema_violation: fields.min_lvl: unknown field for this type"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

// --- CheckValues: re-validate stored rows without touching them ----------

func TestCheckValuesReportsTheSameProblemsAsValidate(t *testing.T) {
	values := map[string]any{"difficulty": "impossible", "nonsense": 1}
	_, want := questSchema().Validate(values)
	got := questSchema().CheckValues(values)
	if want == nil || got == nil {
		t.Fatalf("Validate err = %v, CheckValues err = %v; both must fail", want, got)
	}
	if got.Error() != want.Error() {
		t.Fatalf("CheckValues = %q, Validate = %q; the two must agree exactly", got, want)
	}
	if !errors.Is(got, ErrSchemaViolation) {
		t.Fatalf("errors.Is(%v, ErrSchemaViolation) = false", got)
	}
}

func TestCheckValuesAcceptsARowThatValidateAccepts(t *testing.T) {
	if err := questSchema().CheckValues(map[string]any{"min_level": float64(20)}); err != nil {
		t.Fatalf("CheckValues: %v", err)
	}
}

func TestCheckValuesReturnsNoDataToWriteBack(t *testing.T) {
	// This is the whole point of the method. A schema change flags stored
	// rows invalid without altering them; the only other entry point returns
	// a normalised map with defaults injected, and a caller that forgets to
	// discard it back-fills every row it inspected. CheckValues has nothing
	// to forget.
	//
	// The compile-time shape is the guarantee: CheckValues returns exactly
	// one value, an error. valueChecker below fails to compile the day it
	// returns anything else.
	var checker valueChecker = questSchema()
	if err := checker.CheckValues(map[string]any{"min_level": float64(20)}); err != nil {
		t.Fatalf("CheckValues: %v", err)
	}
}

// valueChecker pins CheckValues's signature: a row in, a verdict out, and no
// data a caller could store by mistake.
type valueChecker interface {
	CheckValues(map[string]any) error
}

func TestCheckValuesDoesNotMutateTheRowItInspects(t *testing.T) {
	// questSchema declares a default for repeatable. Validate would inject
	// it; re-validation must leave the stored row exactly as it was found.
	values := map[string]any{"min_level": float64(20)}
	if err := questSchema().CheckValues(values); err != nil {
		t.Fatalf("CheckValues: %v", err)
	}
	if len(values) != 1 {
		t.Fatalf("values = %v, want the row untouched", values)
	}
	if _, present := values["repeatable"]; present {
		t.Fatalf("values = %v; CheckValues must not back-fill a default into a stored row", values)
	}
}

func TestCheckValuesTreatsAMissingFieldWithADefaultAsSatisfied(t *testing.T) {
	// A stored row that predates a newly declared default is not invalid:
	// the default is what the row would get on its next write. Only a
	// required field with no fallback makes it invalid.
	schema := Schema{
		{Key: "repeatable", Type: FieldBool, HasDefault: true, Default: false},
		{Key: "min_level", Type: FieldNumber, Required: true},
	}
	if err := schema.CheckValues(map[string]any{"min_level": float64(1)}); err != nil {
		t.Fatalf("CheckValues: %v", err)
	}
	err := schema.CheckValues(map[string]any{})
	if err == nil {
		t.Fatal("a row missing a required field with no default must be flagged invalid")
	}
	if err.Error() != "schema_violation: fields.min_level: is required" {
		t.Fatalf("error = %q, want the required-field message", err)
	}
}

// --- Re-review findings ---

func TestParseSchemaWrapsAMalformedSchemaAsErrInvalidSchema(t *testing.T) {
	// The sentinel split exists precisely so a caller need not match on
	// path spelling to tell an invalid declaration from a bad row. The
	// commonest wire failure of all — a field_schema that is not an array,
	// or truncated JSON — must satisfy ErrInvalidSchema like every other
	// declaration failure, not fall through as a bare internal error.
	for name, raw := range map[string][]byte{
		"not an array":  []byte(`{"not":"a list"}`),
		"array of ints": []byte(`[1,2,3]`),
		"truncated":     []byte(`[{"key":"a"`),
	} {
		_, err := ParseSchema(raw)
		if err == nil {
			t.Fatalf("%s: ParseSchema: want an error", name)
		}
		if !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("%s: ParseSchema error = %v, want errors.Is(err, ErrInvalidSchema)", name, err)
		}
	}
}

func TestSchemaRejectsANonFiniteMinimum(t *testing.T) {
	nan := math.NaN()
	msg := checkProblems(t, Schema{{Key: "a", Type: FieldNumber, Min: &nan}})
	if !strings.Contains(msg, "must be finite") {
		t.Fatalf("error = %q, want a finiteness message for a non-finite min", msg)
	}
}

func TestSchemaRejectsANonFiniteMaximum(t *testing.T) {
	inf := math.Inf(1)
	msg := checkProblems(t, Schema{{Key: "a", Type: FieldNumber, Max: &inf}})
	if !strings.Contains(msg, "must be finite") {
		t.Fatalf("error = %q, want a finiteness message for a non-finite max", msg)
	}
}

func TestSchemaRejectsANonFiniteMinimumEvenReachedOnlyFromGo(t *testing.T) {
	// Unreachable over JSON (json.Unmarshal never produces NaN/Inf), but
	// reachable from any Go caller building a Schema in code — which Tasks
	// 3 and 4 both do. coerce's own comment gives the reasoning: NaN
	// compares false against every bound, so an unchecked NaN bound
	// silently disables itself.
	nan := math.NaN()
	schema := Schema{{Key: "a", Type: FieldNumber, Min: &nan}}
	if err := schema.Check(); err == nil {
		t.Fatal("Check must reject a NaN minimum")
	}
}

func TestFieldMarshalJSONRejectsHasDefaultWithANilDefault(t *testing.T) {
	// A field with HasDefault set and a nil Default cannot be spelled on
	// the wire: nil is not a value any field type could hold. The earlier
	// default fix's premise is that a schema may never have been through
	// Check() before it is marshalled — MarshalJSON must not launder that
	// invalid state into a schema that silently round-trips to
	// HasDefault: false, which is what happened before this fix: encoding
	// omitted the "default" key entirely and re-parsing came back with
	// HasDefault false, turning an error into silence.
	f := Field{Key: "a", Type: FieldBool, HasDefault: true, Default: nil}
	if _, err := json.Marshal(f); err == nil {
		t.Fatal("MarshalJSON must reject HasDefault true with a nil Default")
	}
}

func TestSchemaAcceptsAGoNativeStringSliceDefaultOnAListTextField(t *testing.T) {
	// Default: 7 on a number field works because toFloat widens; a Go
	// caller writing the idiomatic []string{"x"} literal for a list<text>
	// default must not be the one surprise left standing. []any and
	// []string must be accepted equally.
	schema := Schema{{Key: "tags", Type: FieldListText, HasDefault: true, Default: []string{"x", "y"}}}
	if err := schema.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	out, err := schema.Validate(map[string]any{})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got, ok := out["tags"].([]any)
	if !ok || len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("tags = %#v, want [x y] as []any", out["tags"])
	}
}

func TestMaxKeyLenBoundary(t *testing.T) {
	if err := (Schema{{Key: strings.Repeat("a", maxKeyLen), Type: FieldText}}).Check(); err != nil {
		t.Fatalf("a key of exactly maxKeyLen characters must be accepted: %v", err)
	}
	msg := checkProblems(t, Schema{{Key: strings.Repeat("a", maxKeyLen+1), Type: FieldText}})
	if !strings.Contains(msg, "key must be at most") {
		t.Fatalf("error = %q, want the key-length message", msg)
	}
}

func TestParseSchemaOfEmptyBytesReturnsAnEmptyNonNilSchema(t *testing.T) {
	s, err := ParseSchema([]byte{})
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if s == nil {
		t.Fatal("ParseSchema([]byte{}) = nil, want a non-nil empty Schema{}")
	}
	if len(s) != 0 {
		t.Fatalf("ParseSchema([]byte{}) = %v, want empty", s)
	}
}

func TestNilSchemaJSONEncodesAsAnEmptyArrayNotNull(t *testing.T) {
	var s Schema
	raw, err := s.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("JSON() on a nil schema = %s, want []", raw)
	}
}

func TestSchemaRejectsEnumWithoutOptionsMessage(t *testing.T) {
	msg := checkProblems(t, Schema{{Key: "a", Type: FieldEnum}})
	if !strings.Contains(msg, "an enum field needs options") {
		t.Fatalf("error = %q, want the enum-needs-options message", msg)
	}
}

func TestSchemaRejectsABadDefaultWithTheDefaultPrefix(t *testing.T) {
	schema := Schema{{Key: "a", Type: FieldBool, HasDefault: true, Default: "yes"}}
	msg := checkProblems(t, schema)
	if !strings.Contains(msg, "default: ") {
		t.Fatalf("error = %q, want the \"default: \" prefix", msg)
	}
}
