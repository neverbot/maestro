// Package paging is the keyset cursor every listing in Maestro pages
// with: its position, its fingerprint, and the clamping rule for a
// requested limit.
//
// It exists because two domains need the same cursor and a copy of a
// cursor is a copy of its bugs. The one this code shipped with omitted
// the project id from its fingerprint, so an unfiltered listing in one
// game produced a cursor that paged perfectly into another game's rows
// and answered with them. That was a wrong answer, not a leak — no row
// crossed the boundary, the *position* did — and it is exactly the kind
// of defect a second copy would inherit and then keep after the first
// was fixed.
//
// It deliberately builds no errors of its own. Every refusal goes
// through a refuse function the caller supplies, so the error is the
// caller's domain type at the caller's own argument path, and the
// *message* still lives here — which is what keeps two domains from
// telling a caller two different things about one bad cursor. Building
// the error here instead would mean importing internal/metamodel, which
// imports this package.
package paging

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Cursor is the keyset position of the last row of a page, together
// with a fingerprint of the listing it was issued for.
//
// **The contract a caller has to know**, which every listing that
// issues one of these must state on its own exported page type (see
// metamodel.EntityPage, which states it for all three of the
// metamodel's listings):
//
//   - A cursor is set when a page came back full, and empty otherwise.
//     A caller looping until the cursor is empty is correct and must
//     expect a final empty page rather than treating one as an error.
//   - A cursor is a *position*, not a snapshot: it is "the rows after
//     this position", never "skip this many rows". That is what makes
//     it stable under concurrent editing — a row inserted or deleted
//     before the position does not slide the window, and the row the
//     cursor names need not still exist. What it does not buy is a
//     consistent view of the whole listing: with a mutable sort key, a
//     row edited to sort after the position can be seen twice, and one
//     edited to sort before it is never seen again by that listing.
//     A caller that needs a consistent whole re-reads from no cursor.
//   - A cursor belongs to the listing that issued it, and to no other:
//     the game, the filter, and a traversal's anchor and direction.
//     Every filter of a listing shares one sort order, so a cursor
//     carried across would page perfectly and answer a different
//     question. Fingerprint is what refuses that.
//   - **It is not a capability and it is not signed.** It is base64 of
//     JSON; a caller can decode it, rewrite the position and recompute
//     the fingerprint from values it already holds. That buys nothing,
//     because the position only ever becomes a `>` comparison inside a
//     statement already filtered by the caller's own project id. The
//     fingerprint is a consistency check against a caller's own
//     mistake and must not be relied on as a security boundary.
//
// Sort is the leading half of the sort key, spelled as text, because a
// cursor is an opaque token whose only job is to come back unchanged,
// and the listing that issued it — pinned by the fingerprint — is the
// only code that has to know how to read it.
//
// The three fields and their wire names are pinned from the metamodel
// side by TestACursorCarriesNothingButAPositionAndAFingerprint, which
// fails if a fourth field is added without a decision about it, and from
// this package's own side by TestTheWireNamesArePinnedHereToo — a
// package whose wire format is guarded only from the outside is one
// deletion away from being unguarded at all.
type Cursor struct {
	Sort        string    `json:"n"`
	ID          uuid.UUID `json:"i"`
	Fingerprint string    `json:"f"`
}

// Encode renders a cursor as the opaque token a caller passes back.
func Encode(c Cursor) string {
	raw, err := json.Marshal(c)
	if err != nil {
		// Unreachable: every field is a string or a uuid.UUID, whose
		// MarshalJSON cannot fail. Returning no cursor rather than
		// panicking keeps a listing answerable if that ever stops being
		// true — a page without a cursor ends the listing, which is a
		// smaller lie than a page with an unreadable one. No test
		// reaches this branch.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// Malformed is what an unreadable cursor is reported as, in the words
// every domain reports it with.
//
// It is exported because a listing can find a cursor unreadable after
// Decode has returned: metamodel.ListRelations parses the position back
// into a timestamp, and a hand-edited cursor whose fingerprint agrees
// can still carry something that is not one. Sharing the sentence is
// the whole point of this package owning the text.
//
// refuse turns the message into the caller's own domain error, at the
// caller's own argument path — `cursor` in internal/metamodel today, and
// in the markdown domain too once Task 8 gives it a listing. Left
// untyped, a bad cursor reaches an agent as internal_error and reads as
// "the server is broken" over a value the agent itself supplied.
//
// **The contract: refuse must never return nil.** A nil result would be
// indistinguishable from success, and the caller two frames up is
// Decode, which would then hand back the zero Cursor with a nil error —
// the position that starts a listing over, silently answering a page-2
// request with page 1 forever. That is the wrong-answer-without-an-error
// class this package exists to prevent, so it is not tolerated from the
// one function callers plug in themselves: Malformed panics rather than
// letting a broken refuse masquerade as a valid cursor.
// TestMalformedPanicsOnANilRefuse pins it.
func Malformed(why string, refuse func(message string) error) error {
	return checkedRefusal(refuse, fmt.Sprintf(
		"is malformed (%s): page from the cursor a previous call returned, or omit it to start",
		why))
}

// checkedRefusal is the one place every refusal in this package goes
// through, so the nil-refuse contract Malformed documents is enforced
// once rather than at each of Decode's four call sites.
func checkedRefusal(refuse func(message string) error, message string) error {
	err := refuse(message)
	if err == nil {
		panic(fmt.Sprintf(
			"paging: refuse(%q) returned nil; a refuse function must always "+
				"build a non-nil error, or Decode's caller would be handed the "+
				"zero position — the start of the listing — for what should "+
				"have been a refusal",
			message))
	}
	return err
}

// Decode reads a page position back and checks it belongs to the listing
// it was handed to.
//
// **The order of the two refusals matters.** A cursor that is not
// readable at all is malformed, and only a readable one can be judged
// against the fingerprint. Reversed, a truncated cursor would be
// reported as belonging to another listing, which sends a caller to
// inspect its filter instead of the value it passed. The order is
// pinned here by
// TestAnUnreadableCursorIsMalformedBeforeItIsJudgedAgainstAFingerprint
// and from the metamodel side by
// TestAMalformedCursorIsRefusedBeforeItsFingerprintIsJudged.
//
// **refuse must never return nil**; see Malformed. Decode enforces the
// same contract on its own fingerprint-mismatch refusal, which does not
// go through Malformed. TestAFingerprintMismatchWithANilRefusePanics
// pins that arm too.
func Decode(s, fingerprint string, refuse func(message string) error) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, Malformed("it is not the encoding this listing issues", refuse)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, Malformed("it does not decode to a page position", refuse)
	}
	if c.ID == uuid.Nil {
		return Cursor{}, Malformed("it carries no row position", refuse)
	}
	if c.Fingerprint != fingerprint {
		return Cursor{}, checkedRefusal(refuse, "was issued for a different listing: page with the filter "+
			"the cursor came from, or omit the cursor to start this listing over")
	}
	return c, nil
}

// Fingerprint digests the resolved filter a cursor was issued under.
//
// **The project id must be the first part, always.** It is not enforced
// here — there is no way to enforce it on a variadic list of strings —
// so every caller passes it first and every listing has a test that a
// cursor from another game is refused (the metamodel's is
// TestACursorFromAnotherGameIsRefused, which covers each of its three
// listings). That is the discipline the original defect was fixed with,
// and the reason this comment is the first thing a new caller reads.
//
// The parts are joined length-prefixed, because without it a filter
// whose parts run together spells the same string as a different filter
// whose parts divide elsewhere, and two listings would share one
// fingerprint. It hashes rather than storing the parts so the cursor
// does not grow with the filter, and truncates to 96 bits because this
// is a consistency check and not a signature.
func Fingerprint(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%d:%s", len(p), p)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// Size turns a caller's requested limit into the one a listing will use:
// asking for nothing is no opinion and gets the default, asking for too
// much is an opinion and gets the cap.
//
// **Clamped, never folded onto the default.** Folding both arms makes
// asking for 501 return strictly fewer rows than asking for 500, with
// nothing in the answer to say so.
func Size(limit, def, max int32) int32 {
	switch {
	case limit <= 0:
		return def
	case limit > max:
		return max
	default:
		return limit
	}
}

// TriState spells an optional boolean filter for a fingerprint, keeping
// "no opinion" distinct from both of the opinions. Without it, a cursor
// issued for "all rows" pages a listing filtered to "invalid rows only",
// which is a different question with the same answer shape.
func TriState(value *bool) string {
	if value == nil {
		return "any"
	}
	return fmt.Sprintf("%t", *value)
}
