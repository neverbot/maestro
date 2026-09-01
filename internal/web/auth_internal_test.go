package web

import "testing"

// TestErrorCodeValuesArePinned asserts the literal wire values of the
// error codes nothing else in the suite happens to assert.
//
// A Task 22 review mutated all twelve values in auth.go's block at once
// and only the registration-path tests failed: six codes are emitted by
// paths no test reads the `error` field of, so their values could have
// been renamed — or fat-fingered — and the whole suite would have stayed
// green while the wire contract changed under a client. Nothing moved;
// this test simply makes that true by assertion rather than by luck.
//
// It pins values, not behaviour: which code a given condition maps to is
// each handler's own test's job. What this refuses to let drift silently
// is the spelling a client parses.
func TestErrorCodeValuesArePinned(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"errCodeUnsupportedMediaType", errCodeUnsupportedMediaType, "unsupported_media_type"},
		{"errCodeRequestTooLarge", errCodeRequestTooLarge, "request_too_large"},
		{"errCodeEmailTaken", errCodeEmailTaken, "email_taken"},
		{"errCodeInviteRequired", errCodeInviteRequired, "invite_required"},
		{"errCodePasswordUnchanged", errCodePasswordUnchanged, "password_unchanged"},
		{"errCodeInviteRequestInvalid", errCodeInviteRequestInvalid, "invite_request_invalid"},
		{"errCodeForbidden", errCodeForbidden, "forbidden"},
		{"errCodeNotFound", errCodeNotFound, "not_found"},
		{"errCodeBadRequest", errCodeBadRequest, "bad_request"},
		{"errCodeSlugTaken", errCodeSlugTaken, "slug_taken"},
		{"errCodeSlugInvalid", errCodeSlugInvalid, "slug_invalid"},
		{"errCodeNameInvalid", errCodeNameInvalid, "name_invalid"},
		{"errCodeInvalidRole", errCodeInvalidRole, "invalid_role"},
		{"errCodeLabelInvalid", errCodeLabelInvalid, "label_invalid"},
		{"errCodeLastAdmin", errCodeLastAdmin, "last_admin"},
		{"errCodeRateLimited", errCodeRateLimited, "rate_limited"},
		{"errCodeEmailInvalid", errCodeEmailInvalid, "email_invalid"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
