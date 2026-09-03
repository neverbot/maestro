package views

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/metamodel"
)

// badChar is one control character, U+0001, placed in every string
// position the language has. **It is a control character and not an
// invalid UTF-8 byte, and the substitution is not a weakening of the
// test.** encoding/json replaces every byte that does not decode with
// U+FFFD rather than refusing it, so a `"\xff"` written into a document
// arrives at the far side of the decode as a valid string and no
// per-position check could ever see it — a test built on one would look
// like it asserted everything while asserting nothing about the bound it
// names, which is this project's second standing defect exactly. (It
// would not pass, either: it would count zero and then report every
// position it wanted, failing for a reason unrelated to what it claims to
// check.) The encoding is therefore
// bounded on the raw bytes, once, by
// TestAnInvalidUTF8ByteIsRefusedBeforeItBecomesAReplacementCharacter, and
// this test uses the fault that does survive a decode to prove that the
// *walk* reaches every position.
//
// It is spelled as the six-character JSON escape rather than as a raw
// byte, because a raw control character inside a JSON string *is* a
// syntax error — the JSON scanner refuses every byte below 0x20, and only
// those — so the document would be refused before the walk ran and this
// test would be exercising the decoder rather than the bound. A raw 0xff
// is not a syntax error, which is why the encoding needs its own check on
// the raw bytes. controlChar below is the same character after the decode.
const badChar = `\u0001`

// controlChar is what badChar decodes to, for the tests that build a Go
// value directly rather than a document.
const controlChar = "\u0001"

// TestEveryStringInAQueryIsBounded is the assertion that closes the
// class. The count is half of it: a new string field added anywhere the
// walk reaches, without a line in `want`, fails this test, which is the
// only mechanism that has ever caught this defect. The bound has been
// missed five times in this project, each fix one step along from the
// last, and each time because the check enumerated the fields somebody
// remembered.
func TestEveryStringInAQueryIsBounded(t *testing.T) {
	doc := `{"v":1,
	  "params":[{"key":"` + badChar + `","type":"text","default":"` + badChar + `"}],
	  "from":[{"type":"` + badChar + `","as":"` + badChar + `","keys":["` + badChar + `"],
	           "where":{"field":"` + badChar + `","op":"` + badChar + `","value":"` + badChar + `"}}],
	  "traverse":[{"from":"` + badChar + `","via":"` + badChar + `","direction":"` + badChar + `",
	               "to_type":"` + badChar + `","as":"` + badChar + `",
	               "where":{"field":"` + badChar + `","op":"eq","value":"` + badChar + `"},
	               "edge_where":{"field":"` + badChar + `","op":"eq","value":"` + badChar + `"}}],
	  "nodes":[{"set":"` + badChar + `","role":"` + badChar + `"}],
	  "edges":[{"via":"` + badChar + `","between":["` + badChar + `","` + badChar + `"],
	            "direction":"` + badChar + `","label_from":"` + badChar + `"}],
	  "project":{"label":"` + badChar + `","color_by":"` + badChar + `",
	             "fields":["` + badChar + `"]}}`

	_, err := ParseQuery([]byte(doc))
	if err == nil {
		t.Fatal("a document whose every string holds a control character must be refused")
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	// Collect the pointers that reported a text problem. Other problems
	// (an unusable set name, an unknown direction) are expected in the
	// same pass and are not counted here.
	got := map[string]bool{}
	for _, f := range qe.Fields {
		if strings.Contains(f.Message, "holds a control character") {
			got[f.Path] = true
		}
	}
	want := []string{
		"/params/0/key", "/params/0/default",
		"/from/0/type", "/from/0/as", "/from/0/keys/0",
		"/from/0/where/field", "/from/0/where/op", "/from/0/where/value",
		"/traverse/0/from", "/traverse/0/via/0", "/traverse/0/direction",
		"/traverse/0/to_type/0", "/traverse/0/as",
		"/traverse/0/where/field", "/traverse/0/where/value",
		"/traverse/0/edge_where/field", "/traverse/0/edge_where/value",
		"/nodes/0/set", "/nodes/0/role",
		"/edges/0/via/0", "/edges/0/between/0", "/edges/0/between/1",
		"/edges/0/direction", "/edges/0/label_from",
		"/project/label", "/project/color_by", "/project/fields/0",
	}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("the string at %s was not bounded: a control character reached the "+
				"rest of the engine from there", ptr)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bounded %d string positions, expected %d; the difference is either a "+
			"position this test forgot or one the walk reaches twice: got %v", len(got), len(want), got)
	}
}

// TestAStringFieldAddedLaterIsBoundedWithoutTouchingTheWalk is the proof
// that this is a closed class rather than a fifth patch. The struct below
// is not one of this package's types and the walk has never heard of it,
// yet a string added to it — at the top level, inside a nested struct,
// inside a slice, inside an `any`, and inside a map — is bounded, and
// reported at the pointer its json tag gives. Nothing in checkAllText or
// walkStrings names any of these fields, so a field added to Query
// tomorrow is covered by exactly the same mechanism.
func TestAStringFieldAddedLaterIsBoundedWithoutTouchingTheWalk(t *testing.T) {
	type addedLaterChild struct {
		AddedDeep string   `json:"added_deep"`
		AddedList []string `json:"added_list"`
		AddedFree any      `json:"added_free"`
		// A field with no member name of its own, the shape AttrRef.Attr
		// has: it must be bounded at its container's pointer, not skipped.
		AddedScalar string            `json:"-"`
		AddedMap    map[string]string `json:"added_map"`
	}
	type addedLater struct {
		AddedTop string           `json:"added_top"`
		Child    *addedLaterChild `json:"child"`
	}

	control := controlChar
	value := &addedLater{
		AddedTop: "top" + control,
		Child: &addedLaterChild{
			AddedDeep:   "deep" + control,
			AddedList:   []string{"list" + control},
			AddedFree:   []any{"free" + control},
			AddedScalar: "scalar" + control,
			AddedMap:    map[string]string{"k": "map" + control},
		},
	}

	got := map[string]bool{}
	for _, f := range checkAllText(value) {
		got[f.Path] = true
	}
	want := []string{
		"/added_top",
		"/child/added_deep",
		"/child/added_list/0",
		"/child/added_free/0",
		"/child", // AddedScalar, at its container's pointer
		"/child/added_map/k",
	}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("a string added at %s escaped the bound; got %v", ptr, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bounded %d positions, expected %d: got %v", len(got), len(want), got)
	}
}

// TestAControlCharacterIsRefusedAnywhereInAQuery is the rule stated over
// the public surface rather than over the walk. A query has no prose
// field, so no control character is allowed anywhere in it, not even a
// newline: every string in a query document is one line of an identifier
// or one literal value.
func TestAControlCharacterIsRefusedAnywhereInAQuery(t *testing.T) {
	// The positive control: the same document with an ordinary set name
	// parses, so a ParseQuery that refused everything cannot pass this.
	if _, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"ab"}]}`)); err != nil {
		t.Fatalf("the same document without the newline must parse: %v", err)
	}
	_, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"a\nb"}]}`))
	if err == nil {
		t.Fatal("a set name holding a newline must be refused")
	}
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("must be query_invalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "holds a control character") {
		t.Fatalf("must name the control character, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	var at string
	for _, f := range qe.Fields {
		if strings.Contains(f.Message, "holds a control character") {
			at = f.Path
		}
	}
	if at != "/from/0/as" {
		t.Fatalf("the control character must be reported at /from/0/as, got %q", at)
	}
}

// TestAnInvalidUTF8ByteIsRefusedBeforeItBecomesAReplacementCharacter is
// the other half of the encoding rule, and it is a whole-document check
// for a reason the test itself demonstrates: after the decode the
// evidence is gone. encoding/json turns the invalid byte into U+FFFD, so
// the assertion below is that ParseQuery refuses the *bytes*, and the
// second half proves the substitution is real rather than assumed.
func TestAnInvalidUTF8ByteIsRefusedBeforeItBecomesAReplacementCharacter(t *testing.T) {
	if _, err := ParseQuery([]byte(`{"v":1,"from":[{"type":"quest","as":"ab"}]}`)); err != nil {
		t.Fatalf("the same document with valid bytes must parse: %v", err)
	}
	_, err := ParseQuery([]byte("{\"v\":1,\"from\":[{\"type\":\"quest\",\"as\":\"a\xffb\"}]}"))
	if err == nil {
		t.Fatal("a document holding a byte that is not valid UTF-8 must be refused")
	}
	if !errors.Is(err, ErrQueryInvalid) {
		t.Fatalf("must be query_invalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "is not valid UTF-8") {
		t.Fatalf("must say the document is not valid UTF-8, got %v", err)
	}

	// The reason the check cannot be per-string: the decoder replaces the
	// byte, so metamodel.CheckText — the shared judgement — sees nothing
	// wrong with what comes out. If this ever stops being true, the
	// whole-document check can move into the walk and gain a pointer.
	var probe struct {
		A string `json:"a"`
	}
	if err := json.Unmarshal([]byte("{\"a\":\"x\xffy\"}"), &probe); err != nil {
		t.Fatalf("the decoder must accept the byte rather than refuse it: %v", err)
	}
	if _, bad := metamodel.CheckText(probe.A, ""); bad {
		t.Fatalf("the decoded string is expected to be valid UTF-8 after substitution, got %q", probe.A)
	}
	if !strings.ContainsRune(probe.A, '�') {
		t.Fatalf("the decoder is expected to substitute U+FFFD, got %q", probe.A)
	}
}

// TestAnOverlongStringIsRefusedWhereverItSits pins the length cap on a
// position no narrower rule covers — a literal value inside `value`,
// which is an `any` and so is invisible to any check over typed string
// fields.
func TestAnOverlongStringIsRefusedWhereverItSits(t *testing.T) {
	long := strings.Repeat("a", MaxStringLen+1)
	doc := `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"eq","value":"` + long + `"}}]}`
	_, err := ParseQuery([]byte(doc))
	if err == nil {
		t.Fatal("an overlong value must be refused")
	}
	if !strings.Contains(err.Error(), "must be at most 4096 bytes") {
		t.Fatalf("must name the cap, got %v", err)
	}
	var qe *QueryError
	if !errors.As(err, &qe) {
		t.Fatalf("must be a *QueryError, got %T", err)
	}
	if len(qe.Fields) != 1 || qe.Fields[0].Path != "/from/0/where/value" {
		t.Fatalf("the cap must be reported at /from/0/where/value, got %v", qe.Fields)
	}
	// One byte shorter is accepted, so the cap is the cap and not an
	// accident of the document being long at all.
	ok := `{"v":1,"from":[{"type":"quest","where":{"field":"rank","op":"eq","value":"` +
		strings.Repeat("a", MaxStringLen) + `"}}]}`
	if _, err := ParseQuery([]byte(ok)); err != nil {
		t.Fatalf("a value of exactly %d bytes must be accepted: %v", MaxStringLen, err)
	}
}

// The three shapes TestAnEmbeddedTypesPromotedFieldIsBounded needs.
// embeddedUnexported is deliberately an *unexported type*: reflection
// reports the anonymous field holding it as unexported, while
// encoding/json promotes and populates its exported fields as ordinary
// top-level members of the document. A walk that read `IsExported` as a
// statement about document membership would drop `promoted_hidden` on the
// floor, which is the seventh instance of this project's standing defect
// and the one hiding inside the mechanism written to close the class.
type embeddedUnexported struct {
	PromotedHidden string `json:"promoted_hidden"`
}

// EmbeddedExported is the other half: the field is exported, so the walk
// reached it before — but at `/EmbeddedExported/promoted_visible`, which
// is the *Go* name of a type that never appears in the caller's document.
type EmbeddedExported struct {
	PromotedVisible string `json:"promoted_visible"`
}

type embeddingHost struct {
	embeddedUnexported
	EmbeddedExported
	Named string `json:"named"`
	// Not a member of the document at all: encoding/json can neither read
	// nor write it, so nothing a caller wrote can be in it.
	notAMember string
}

// TestAnEmbeddedTypesPromotedFieldIsBounded closes the seventh instance
// of the class this walk exists to close, and it is latent only because
// no document type embeds anything *today*: fourteen later tasks add
// types under this walk's promise that a walk cannot forget a field, and
// sharing a common struct across them is the most natural refactor there
// is.
//
// The test asserts three things at once, and the first is what makes the
// other two mean anything:
//
//  1. **All three strings are really members of the decoded document.**
//     The decode is done here rather than assumed, so if encoding/json
//     ever stops promoting an unexported type's exported fields this test
//     says so rather than quietly pinning a walk of something nobody can
//     write.
//  2. Both promoted strings are bounded.
//  3. Both are reported **at the container's own pointer** — `/named`'s
//     neighbours, not `/EmbeddedExported/promoted_visible`. The pointer is
//     the whole product of a QueryError: an agent that gets one knows
//     which member to rewrite, and an embedded type's Go name is not a
//     member it can find.
func TestAnEmbeddedTypesPromotedFieldIsBounded(t *testing.T) {
	var host embeddingHost
	doc := `{"promoted_hidden":"a` + badChar + `","promoted_visible":"b` + badChar +
		`","named":"c` + badChar + `"}`
	if err := json.Unmarshal([]byte(doc), &host); err != nil {
		t.Fatalf("the document must decode: %v", err)
	}
	// Read through the promoted names rather than through the embedded
	// field, because promotion is the thing under test: if encoding/json
	// ever stops promoting, these stop compiling or stop being populated,
	// and either way the walk's embedded arm is pinning something
	// unreachable rather than guarding a real document member.
	if host.PromotedHidden == "" {
		t.Fatal("encoding/json is expected to promote an unexported type's exported field: " +
			"if it no longer does, the walk's embedded arm is pinning something unreachable")
	}
	if host.PromotedVisible == "" {
		t.Fatal("an exported embedded type's field must be populated by the decode")
	}
	host.notAMember = "d" + controlChar

	got := map[string]bool{}
	for _, f := range checkAllText(&host) {
		got[f.Path] = true
	}
	want := []string{"/promoted_hidden", "/promoted_visible", "/named"}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("the string at %s was not bounded at the pointer the caller wrote it at; "+
				"got %v", ptr, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bounded %d positions, expected %d: a field the document does not have is "+
			"being walked, or one it does have is not; got %v", len(got), len(want), got)
	}
}

// TestADeferredRawMessageIsBounded pins the one byte slice whose contents
// are caller text. A json.RawMessage is a document member the decode
// postponed, not bytes: walking it as the []byte it is would visit
// numbers and bound nothing, and no later stage would ever look at it
// again. Nothing uses one today; Task 3's predicate values and Task 11's
// stored documents are both natural places for one to appear, and it
// would silently reopen the class.
//
// The second half of the test states the other side of the rule the
// walk's comment carries: a plain []byte is *not* caller text, because it
// decodes from base64 rather than from a string the caller wrote, and it
// is not walked.
func TestADeferredRawMessageIsBounded(t *testing.T) {
	type deferredDoc struct {
		Raw   json.RawMessage `json:"raw"`
		Bytes []byte          `json:"bytes"`
		Named string          `json:"named"`
	}
	var doc deferredDoc
	body := `{"raw":{"deep":["x` + badChar + `"]},"bytes":"AAEC","named":"y` + badChar + `"}`
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("the document must decode: %v", err)
	}
	if doc.Bytes[1] != 0x01 {
		t.Fatalf("the []byte member is expected to decode from base64, got %v", doc.Bytes)
	}
	got := map[string]bool{}
	for _, f := range checkAllText(&doc) {
		got[f.Path] = true
	}
	want := []string{"/raw/deep/0", "/named"}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("the string at %s escaped the bound; got %v", ptr, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bounded %d positions, expected %d: got %v", len(got), len(want), got)
	}
}

// TestTheWalkReachesAnArrayAndAMapKey pins three arms of the walk that
// nothing else in this file distinguishes: a Go array (as opposed to a
// slice), a map *key* (as opposed to its value), and the skip that keeps
// a field the document does not have out of the report.
func TestTheWalkReachesAnArrayAndAMapKey(t *testing.T) {
	type arrayAndMap struct {
		Fixed [2]string         `json:"fixed"`
		Table map[string]string `json:"table"`
	}
	value := &arrayAndMap{
		Fixed: [2]string{"one" + controlChar, "two" + controlChar},
		// The key holds the fault and the value does not: a walk that
		// visited only values would report nothing here.
		Table: map[string]string{"key" + controlChar: "clean"},
	}
	got := map[string]bool{}
	for _, f := range checkAllText(value) {
		got[f.Path] = true
	}
	want := []string{"/fixed/0", "/fixed/1", "/table/key" + controlChar}
	for _, ptr := range want {
		if !got[ptr] {
			t.Errorf("the string at %q escaped the bound; got %v", ptr, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bounded %d positions, expected %d: got %v", len(got), len(want), got)
	}
}
