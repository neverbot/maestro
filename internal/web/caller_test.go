package web

import (
	"testing"

	"github.com/google/uuid"

	"github.com/neverbot/maestro/internal/assert"
)

// TestTokenCallerShape pins the one shape newTokenCaller ever produces:
// TokenID and ProjectID both set from the same resolved token, IsToken
// true, and ScopedProject returning that exact project.
func TestTokenCallerShape(t *testing.T) {
	t.Parallel()
	userID, tokenID, projectID := uuid.New(), uuid.New(), uuid.New()
	c := newTokenCaller(userID, true, tokenID, projectID)

	assert.Must(t, c.IsToken(), "IsToken() = false, want true for a token caller")
	got, ok := c.ScopedProject()
	assert.Must(t, ok && got == projectID, "ScopedProject() = (%v, %v), want (%v, true)", got, ok, projectID)
	assert.Must(t, c.UserID == userID, "UserID = %v, want %v", c.UserID, userID)
	assert.Must(t, c.IsAdmin, "IsAdmin = false, want true as constructed")
	assert.Must(t, c.TokenID != nil && *c.TokenID == tokenID, "TokenID = %v, want %v", c.TokenID, tokenID)
}

// TestSessionCallerShape pins the other shape newSessionCaller ever
// produces: TokenID and ProjectID both nil, IsToken false, ScopedProject
// reporting "no project" rather than the zero UUID being mistaken for one.
func TestSessionCallerShape(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	c := newSessionCaller(userID, false)

	assert.Must(t, !c.IsToken(), "IsToken() = true, want false for a session caller")
	if _, ok := c.ScopedProject(); ok {
		t.Fatal("ScopedProject() ok = true, want false: a session caller carries no project")
	}
	assert.Must(t, c.TokenID == nil, "TokenID = %v, want nil", c.TokenID)
	assert.Must(t, c.ProjectID == nil, "ProjectID = %v, want nil", c.ProjectID)
}

// TestScopedProjectIgnoresIsAdmin pins the property this task exists to
// protect, encoded once here instead of at every downstream call site: an
// admin's token is still scoped to exactly the project it is bound to.
// ScopedProject must not special-case IsAdmin into an unscoped pass.
func TestScopedProjectIgnoresIsAdmin(t *testing.T) {
	t.Parallel()
	projectID := uuid.New()
	c := newTokenCaller(uuid.New(), true, uuid.New(), projectID)

	got, ok := c.ScopedProject()
	assert.Must(t, ok && got == projectID, "ScopedProject() = (%v, %v), want (%v, true) even though IsAdmin is true", got, ok, projectID)
}
