package web

import (
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// TestErrorCodeValuesArePinned asserts the literal wire values of the
// error codes nothing else in the suite happens to assert.
func TestErrorCodeValuesArePinned(t *testing.T) {
	t.Parallel()
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
		assert.Should(t, tc.got == tc.want, "%s = %q, want %q", tc.name, tc.got, tc.want)
	}
}
