package web

import (
	"testing"

	"github.com/google/uuid"
)

// TestTokenCallerShape pins the one shape newTokenCaller ever produces:
// TokenID and ProjectID both set from the same resolved token, IsToken
// true, and ScopedProject returning that exact project.
func TestTokenCallerShape(t *testing.T) {
	userID, tokenID, projectID := uuid.New(), uuid.New(), uuid.New()
	c := newTokenCaller(userID, true, tokenID, projectID)

	if !c.IsToken() {
		t.Fatal("IsToken() = false, want true for a token caller")
	}
	got, ok := c.ScopedProject()
	if !ok || got != projectID {
		t.Fatalf("ScopedProject() = (%v, %v), want (%v, true)", got, ok, projectID)
	}
	if c.UserID != userID {
		t.Fatalf("UserID = %v, want %v", c.UserID, userID)
	}
	if !c.IsAdmin {
		t.Fatal("IsAdmin = false, want true as constructed")
	}
	if c.TokenID == nil || *c.TokenID != tokenID {
		t.Fatalf("TokenID = %v, want %v", c.TokenID, tokenID)
	}
}

// TestSessionCallerShape pins the other shape newSessionCaller ever
// produces: TokenID and ProjectID both nil, IsToken false, ScopedProject
// reporting "no project" rather than the zero UUID being mistaken for one.
func TestSessionCallerShape(t *testing.T) {
	userID := uuid.New()
	c := newSessionCaller(userID, false)

	if c.IsToken() {
		t.Fatal("IsToken() = true, want false for a session caller")
	}
	if _, ok := c.ScopedProject(); ok {
		t.Fatal("ScopedProject() ok = true, want false: a session caller carries no project")
	}
	if c.TokenID != nil {
		t.Fatalf("TokenID = %v, want nil", c.TokenID)
	}
	if c.ProjectID != nil {
		t.Fatalf("ProjectID = %v, want nil", c.ProjectID)
	}
}

// TestScopedProjectIgnoresIsAdmin pins the property this task exists to
// protect, encoded once here instead of at every downstream call site: an
// admin's token is still scoped to exactly the project it is bound to.
// ScopedProject must not special-case IsAdmin into an unscoped pass.
func TestScopedProjectIgnoresIsAdmin(t *testing.T) {
	projectID := uuid.New()
	c := newTokenCaller(uuid.New(), true, uuid.New(), projectID)

	got, ok := c.ScopedProject()
	if !ok || got != projectID {
		t.Fatalf("ScopedProject() = (%v, %v), want (%v, true) even though IsAdmin is true", got, ok, projectID)
	}
}
