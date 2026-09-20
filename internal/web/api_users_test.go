package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
)

// TestOnlyAnAdministratorReadsTheAccounts is the gate, and it is the
// first thing to assert about a route that publishes every address on
// the instance.
func TestOnlyAnAdministratorReadsTheAccounts(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "first@example.test", DisplayName: "First", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "second@example.test", DisplayName: "Second", Password: "password12345"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.AddCookie(loginAs(t, srv, "second@example.test"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a plain account reading /api/users: status = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// And an unauthenticated caller gets nothing either.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous caller reading /api/users: status = %d, want 401", rec.Code)
	}
}

// TestTheAccountListingSaysWhoIsWhoAndPages covers what the screen reads
// off it: the standing, the caller's own row, and the cursor.
func TestTheAccountListingSaysWhoIsWhoAndPages(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	// The first account made on an instance is its administrator, which
	// is BootstrapFirstAdmin's rule and is what makes this caller one.
	admin, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "admin@example.test", DisplayName: "Admin", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := ids.SetAdmin(ctx, admin.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}
	for _, email := range []string{"b@example.test", "c@example.test"} {
		if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{Email: email, DisplayName: strings.ToUpper(email[:1]), Password: "password12345"}); err != nil {
			t.Fatalf("CreateUser %s: %v", email, err)
		}
	}
	cookie := loginAs(t, srv, "admin@example.test")

	read := func(query string) (string, []map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/users"+query, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list users%s: status = %d: %s", query, rec.Code, rec.Body.String())
		}
		var body struct {
			Users      []map[string]any `json:"users"`
			NextCursor string           `json:"next_cursor"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode listing: %v", err)
		}
		return body.NextCursor, body.Users
	}

	_, all := read("")
	if len(all) != 3 {
		t.Fatalf("the instance has 3 accounts and the listing answered %d", len(all))
	}
	// Oldest first, so the list does not reshuffle under a reader every
	// time somebody signs up.
	if all[0]["email"] != "admin@example.test" {
		t.Errorf("the listing is not in creation order: %+v", all)
	}
	mine := 0
	admins := 0
	for _, user := range all {
		if user["you"] == true {
			mine++
		}
		if user["is_admin"] == true {
			admins++
		}
		if _, leaked := user["password_hash"]; leaked {
			t.Fatal("the listing publishes a password hash")
		}
	}
	if mine != 1 {
		t.Errorf("%d rows are marked as the caller's own, want exactly 1", mine)
	}
	if admins != 1 {
		t.Errorf("%d rows are marked administrator, want exactly 1", admins)
	}

	// **A cursor is a bookmark and pages once.** A page that answered
	// the same rows again would loop the screen for ever.
	cursor, first := read("?page_size=2")
	if len(first) != 2 || cursor == "" {
		t.Fatalf("first page = %d rows, cursor %q, want 2 and a cursor", len(first), cursor)
	}
	next, second := read("?page_size=2&cursor=" + cursor)
	if len(second) != 1 {
		t.Fatalf("second page = %d rows, want the remaining 1", len(second))
	}
	if second[0]["id"] == first[0]["id"] || second[0]["id"] == first[1]["id"] {
		t.Errorf("the second page repeats a row from the first")
	}
	if next != "" {
		t.Errorf("the last page still offers a cursor: %q", next)
	}

	// A cursor this listing did not issue is refused rather than answered
	// with the first page, which would page a caller in a circle.
	req := httptest.NewRequest(http.MethodGet, "/api/users?cursor=not-a-cursor", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a made-up cursor: status = %d, want 400", rec.Code)
	}
}

// TestAnAccountIsEditedByItsIDAndKeepsTheLastAdministrator is the write
// half: the address somebody signs in with, the name everybody sees, the
// standing — and the one rule none of it may go around.
func TestAnAccountIsEditedByItsIDAndKeepsTheLastAdministrator(t *testing.T) {
	t.Parallel()
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()
	admin, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "admin@example.test", DisplayName: "Admin", Password: "password12345"})
	if err := ids.SetAdmin(ctx, admin.ID, true); err != nil {
		t.Fatalf("SetAdmin: %v", err)
	}
	other, _ := ids.CreateUser(ctx, identity.CreateUserRequest{Email: "other@example.test", DisplayName: "Other", Password: "password12345"})
	cookie := loginAs(t, srv, "admin@example.test")

	patch := func(id string, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/users/"+id, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.NewDecoder(rec.Body).Decode(&out)
		return rec.Code, out
	}

	// The name and the address, together.
	code, body := patch(other.ID.String(), `{"display_name":"Renamed","email":"renamed@example.test"}`)
	if code != http.StatusOK {
		t.Fatalf("edit: status = %d: %+v", code, body)
	}
	if body["display_name"] != "Renamed" || body["email"] != "renamed@example.test" {
		t.Fatalf("the answer does not carry the edit: %+v", body)
	}
	// **The address is how they sign in**, so the change has to have
	// reached the thing that authenticates: the old one must stop
	// working and the new one must start.
	if _, err := ids.Authenticate(ctx, "other@example.test", "password12345"); err == nil {
		t.Error("the old address still signs in after the account was moved")
	}
	if _, err := ids.Authenticate(ctx, "renamed@example.test", "password12345"); err != nil {
		t.Errorf("the new address does not sign in: %v", err)
	}

	// An address another account already uses is refused, and nothing
	// changes.
	code, _ = patch(other.ID.String(), `{"email":"admin@example.test"}`)
	if code != http.StatusConflict {
		t.Errorf("moving an account onto a taken address: status = %d, want 409", code)
	}

	// The standing, and the rule behind it: the instance keeps one
	// administrator. Promoting the other first is what makes the
	// demotion legal, which is exactly the path the screen offers.
	if code, body = patch(other.ID.String(), `{"is_admin":true}`); code != http.StatusOK || body["is_admin"] != true {
		t.Fatalf("promoting: status = %d, body %+v", code, body)
	}
	if code, _ = patch(admin.ID.String(), `{"is_admin":false}`); code != http.StatusOK {
		t.Fatalf("demoting with another administrator standing: status = %d", code)
	}
	// **Demoting yourself takes your own standing with it**, which is
	// why the screen refuses that one control on your own row: the very
	// next request from this caller is a 403, because they are no longer
	// an administrator. Asserted, because it is the reason for a
	// disabled checkbox that would otherwise look like caution.
	if code, _ = patch(other.ID.String(), `{"display_name":"Nope"}`); code != http.StatusForbidden {
		t.Errorf("an administrator who demoted themselves: status = %d, want 403", code)
	}

	// And the last administrator standing cannot take it off either —
	// identity's own guard, reached through this route. `other` is that
	// administrator now, so it is `other` who has to ask.
	cookie = loginAs(t, srv, "renamed@example.test")
	code, body = patch(other.ID.String(), `{"is_admin":false}`)
	if code != http.StatusConflict {
		t.Fatalf("demoting the last administrator: status = %d, want 409: %+v", code, body)
	}
	if body["error"] != "last_admin" {
		t.Errorf("the refusal is not the last-administrator one: %+v", body)
	}

	// An id that names nobody, and a malformed one, are both "no such
	// user" rather than a 500.
	if code, _ = patch("00000000-0000-0000-0000-000000000000", `{"display_name":"Ghost"}`); code != http.StatusNotFound {
		t.Errorf("editing an unknown id: status = %d, want 404", code)
	}
	if code, _ = patch("not-a-uuid", `{"display_name":"Ghost"}`); code != http.StatusNotFound {
		t.Errorf("editing a malformed id: status = %d, want 404", code)
	}
}
