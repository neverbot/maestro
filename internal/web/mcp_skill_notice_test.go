package web_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/skill"
)

// instructionsOf initialises an MCP session against a real server and
// returns what that server told the client it was. Through the
// transport and not through the function: instructions a server never
// sends are exactly the defect this guard exists for.
func instructionsOf(t *testing.T) string {
	t.Helper()
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	t.Cleanup(httpSrv.Close)
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "notice@example.test", DisplayName: "Owner", Password: "password12345"})
	assert.NoErr(t, err, "CreateUser")
	game, err := projSvc.Create(ctx, "azeroth", "Azeroth", user.ID)
	assert.NoErr(t, err, "Create")
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent"})
	assert.NoErr(t, err, "CreateAPIToken")

	session := connectMCP(t, httpSrv.URL, token)
	result := session.InitializeResult()
	assert.Must(t, result != nil, "the session carries no initialize result")
	return result.Instructions
}

// **An agent learns its skills are stale without being asked to look.**
// The bundle cannot tell anybody it is out of date — an agent reading an
// outdated page reads outdated instructions, and a bundle predating this
// check would never learn to run it — so the server says it, at the one
// moment every agent passes through.
func TestConnectingSaysWhichSkillBundleIsCurrent(t *testing.T) {
	t.Parallel()
	instructions := instructionsOf(t)
	assert.Must(t, instructions != "", "the server sends no instructions, so nothing tells an agent its pages are stale")
	assert.Must(t, strings.Contains(instructions, skill.Version()),
		"the instructions do not carry the current bundle version %s:\n%s", skill.Version(), instructions)
	for _, part := range []string{skill.ManifestName, "skill.install", "restart"} {
		assert.Must(t, strings.Contains(instructions, part),
			"the instructions never mention %q, so an agent is told a version and not what to do with it", part)
	}
}

// The version announced is the hash of the file that ships, so the check
// the instructions describe is one an agent can actually run.
func TestTheAnnouncedVersionIsTheHashOfTheShippedManifest(t *testing.T) {
	t.Parallel()
	manifest, err := skill.Manifest()
	assert.NoErr(t, err, "rendering the manifest")
	assert.Must(t, skill.VersionOf(manifest) == skill.Version(),
		"the announced version is %s and hashing the manifest gives %s", skill.Version(), skill.VersionOf(manifest))
}
