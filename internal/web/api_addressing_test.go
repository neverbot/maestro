package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/web"
)

// TestAGameIsAddressedByItsSlugAndNotByItsID is the decision, pinned.
//
// The routes took a uuid while /g/{slug} took a slug, so a person who
// had just created a game and was looking at it by name had to go and
// find a uuid before they could mint a token or seed anything. The slug
// now *replaces* the id here rather than being accepted beside it — the
// call the metamodel made for row keys, taken for the same reason: two
// names for one thing cost every caller a decision and buy nothing.
//
// So both halves are asserted: the slug works, and the id does not.
func TestAGameIsAddressedByItsSlugAndNotByItsID(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	rec := sessionGet(t, srv, cookie, "/api/games/"+game.Slug+"/members")
	if rec.Code != http.StatusOK {
		t.Fatalf("the slug address = %d: %s", rec.Code, rec.Body.String())
	}

	// The id is not an address, and the refusal says so rather than
	// leaving a caller holding the right game and the wrong name for it
	// to conclude the game does not exist.
	rec = sessionGet(t, srv, cookie, "/api/games/"+game.ID.String()+"/members")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the id address = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, game.ID.String()) {
		t.Fatalf("the refusal does not say what was tried: %s", body)
	}
	if !strings.Contains(body, "addressed by its slug") || !strings.Contains(body, "not by its id") {
		t.Fatalf("the refusal does not say the identifier is the wrong kind: %s", body)
	}
}

// TestTheSlugAddressFoldsCase. projects_slug_key is UNIQUE (lower(slug))
// and /g/Azeroth is the same page as /g/azeroth, so the routes must
// agree — a designer who capitalised a bookmark is not looking at a
// different game.
func TestTheSlugAddressFoldsCase(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	for _, ref := range []string{"azeroth", "Azeroth", "AZEROTH"} {
		rec := sessionGet(t, srv, cookie, "/api/games/"+ref+"/members")
		if rec.Code != http.StatusOK {
			t.Fatalf("%q = %d: %s", ref, rec.Code, rec.Body.String())
		}
	}
}

// TestASlugThatNamesNothingAndOneYouAreNotInAreTheSameRefusal is the
// property the whole shape of resolveGameRef exists to protect.
//
// A uuid is not guessable, so the old routes could safely answer "you
// are not a member of this game" — the caller had to be holding the id
// already. A slug is a name someone chose, so the same answer would be
// an enumeration oracle: a stranger could walk plausible names and learn
// which studios exist here, one guess at a time. BySlugForUser joins
// membership into the lookup so both cases are one answer, and this is
// what proves it stays that way.
func TestASlugThatNamesNothingAndOneYouAreNotInAreTheSameRefusal(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	ctx := context.Background()

	owner, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := projSvc.Create(ctx, "azeroth", "Azeroth", owner.ID); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "stranger@studio.com", DisplayName: "Stranger", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "stranger@studio.com")

	answers := map[string]string{}
	for _, ref := range []string{"azeroth", "there-is-no-such-game"} {
		rec := sessionGet(t, srv, cookie, "/api/games/"+ref+"/members")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%q = %d, want 404: %s", ref, rec.Code, rec.Body.String())
		}
		var payload struct{ Error, Message string }
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %q: %v", ref, err)
		}
		if payload.Error != "not_found" {
			t.Fatalf("%q error = %q, want not_found", ref, payload.Error)
		}
		// The message names the caller's own input and nothing else, so
		// what is compared is the sentence with that input removed.
		answers[ref] = strings.Replace(payload.Message, ref, "<ref>", 1)
	}
	if answers["azeroth"] != answers["there-is-no-such-game"] {
		t.Fatalf("a real game a stranger is not in answers %q and a game that does not exist "+
			"answers %q: the two must be indistinguishable",
			answers["azeroth"], answers["there-is-no-such-game"])
	}
	if !strings.Contains(answers["azeroth"], "available to you") {
		t.Fatalf("the refusal reads %q, and must not claim the game does not exist",
			answers["azeroth"])
	}
}

// TestAnAgentReachesRESTWithNothingButItsTokenAndItsGamesSlug is the
// other half of the complaint this change answers. An agent is handed a
// token bound to one game; before this, it still had to learn that
// game's uuid before its first REST call, and the only way to learn one
// was another call. whoami's project_slug is now that address, and this
// drives exactly that path with no id anywhere in it.
func TestAnAgentReachesRESTWithNothingButItsTokenAndItsGamesSlug(t *testing.T) {
	f := newMetamodelFixture(t)
	ctx := context.Background()

	whoami, err := web.MCPWhoami(ctx, f.deps, f.caller)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if whoami.ProjectSlug == "" {
		t.Fatal("whoami carries no project_slug, and the slug is the address")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/games/"+whoami.ProjectSlug+"/types", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an agent addressing its own game by the slug whoami gave it = %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestATokenNamingAnotherGamesSlugIsToldWhichGameItIsBoundTo. A token
// caller is never resolved by the name it typed — its game is fixed at
// the moment the token was minted — so the question is only whether the
// name agrees with the binding. Both slugs are the caller's own, so the
// refusal can name both, which is what turns "no" into "you are pointing
// at the wrong game".
func TestATokenNamingAnotherGamesSlugIsToldWhichGameItIsBoundTo(t *testing.T) {
	f := newMetamodelFixture(t)

	for _, ref := range []string{f.otherSlug, "a-game-nobody-has"} {
		req := httptest.NewRequest(http.MethodGet, "/api/games/"+ref+"/types", nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		// A scope violation for both, and not a not_found for the second:
		// a token caller resolves nothing, so it can learn nothing about
		// what exists, and both refusals are about its own binding.
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%q = %d, want 403: %s", ref, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "scope_violation") {
			t.Fatalf("%q answered %s, want scope_violation", ref, body)
		}
		if !strings.Contains(body, f.gameSlug) || !strings.Contains(body, ref) {
			t.Fatalf("%q answered %s, want it to name both the binding and what was asked for",
				ref, body)
		}
	}
}

// TestAGameCreatedByNameIsImmediatelyReachableByThatName is the sequence
// the complaint describes end to end: create a game with a slug and a
// name, then use it. There is no id in this test at all, which is the
// point — nothing between "I made a game" and "I am working in it" asks
// for one.
func TestAGameCreatedByNameIsImmediatelyReachableByThatName(t *testing.T) {
	srv, ids, _ := newTestServer(t)
	ctx := context.Background()

	if _, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "owner@studio.com", DisplayName: "Owner", Password: "password12345",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cookie := loginAs(t, srv, "owner@studio.com")

	create := jsonRequest(http.MethodPost, "/api/games", `{"slug":"le-mans","name":"Le Mans"}`)
	create.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}

	mint := jsonRequest(http.MethodPost, "/api/games/le-mans/tokens", `{"label":"agent"}`)
	mint.AddCookie(cookie)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, mint)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("minting a token at the game's own name = %d: %s", rec.Code, rec.Body.String())
	}
}

// sessionGet is a GET as a logged-in designer. Every addressing
// assertion here goes through it, so what differs between the cases is
// the address and nothing else.
func sessionGet(t *testing.T, srv *web.Server, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
