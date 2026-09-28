// Package paging is the keyset cursor every listing in Maestro pages
// with: its position, its fingerprint, and the clamping rule for a
// requested limit.
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
