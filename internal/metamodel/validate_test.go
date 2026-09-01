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

func TestParseSchemaIgnoresHasDefaultOnTheWire(t *testing.T) {
	// has_default is not an input key. Accepting it would give the wire two
	// sources of truth for one fact, which is what caused this bug: the
	// presence of "default" is the only declaration.
	back, err := ParseSchema([]byte(`[{"key":"repeatable","type":"bool","has_default":true}]`))
	if err != nil {
		t.Fatalf("ParseSchema: %v", err)
	}
	if back[0].HasDefault {
		t.Fatalf("field = %#v, want has_default on the wire to be ignored", back[0])
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
