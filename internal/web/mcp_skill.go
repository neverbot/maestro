package web

import (
	"context"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/skill"
)

// This file is the delivery half of the skill bundle: one tool that says
// where the bundle is and what version it is, and nothing that carries
// the bundle itself.
//
// **The version an agent is told here and the version whoami reports are
// one fact from one source.** Both read skill.Version().
// TestWhoamiReportsTheVersionSkillInstallServes drives them through one
// session and compares them, because two fields carrying one fact from
// two sources is the defect this repository spent a sub-project removing.
//
// **The tests in this repository keep the bundle honest against *this*
// server; the handshake keeps an installed bundle honest against a server
// that has moved since.** Neither covers the other. A guard that
// regenerates reference/tools.md says nothing about the copy a designer
// extracted three months ago into a skills directory, and a version
// comparison says nothing about whether today's pages are true.
//
// **v1 is install-only: there is no skill.read.** A bundle read over MCP
// costs its full size in the context window every time, which is the
// whole reason download_url exists, and no host that cannot write files
// has asked for one. What would change if one were wanted: a second tool
// returning one page by path, bounded, with the same version beside it.
//
// **v1 has no overrides.** Maestro's markdown domain has no global scope
// — every document is project-scoped — so there is nowhere for a
// per-instance page to live that is not per-game, and a per-game override
// of a page teaching the metamodel is a game rewriting the rules of the
// product. What would change if one were wanted: a global document scope
// first, then a merge order stated here, then a version that covers both
// layers rather than the embedded tree alone.

// SkillInstallTarget is where the bundle goes and what to do with it.
type SkillInstallTarget struct {
	Name         string `json:"name"`
	PreferredDir string `json:"preferred_dir"`
	FallbackDir  string `json:"fallback_dir"`
	Instructions string `json:"instructions"`
}

// SkillInstallOutput is the descriptor skill.install answers with.
//
// **It carries no bundle bytes and it is asserted not to.**
// TestTheInstallDescriptorCarriesNoBundleBytes marshals it and checks
// both its size and the absence of a zip's own magic number, which is the
// one property of this whole mechanism nothing else in the repository
// would notice the loss of.
type SkillInstallOutput struct {
	DownloadURL   string             `json:"download_url"`
	ExpiresAt     string             `json:"expires_at"`
	Format        string             `json:"format"`
	BundleVersion string             `json:"bundle_version"`
	Install       SkillInstallTarget `json:"install"`
}

// skillInstallInstructions is the "what do I do with this" prose, carried
// in the descriptor rather than left to the bundle. The first install is
// the one where the pages are not on disk yet, so a descriptor that
// assumed they were would be useless exactly once — on the only call that
// matters.
const skillInstallInstructions = "Fetch download_url with any HTTP tool the host gives you " +
	"(curl, wget, Invoke-WebRequest, Python urllib, Node fetch) — it needs no Authorization " +
	"header. Unzip it into preferred_dir, overwriting what is there; if you cannot write " +
	"there, use fallback_dir. Create the directory if it does not exist. Write " +
	"bundle_version into a manifest beside the files, and skip the download next time the " +
	"version you are told matches the one you stored."

type skillInstallInput struct{ ScopedArgs }

// MCPSkillInstall implements skill.install.
//
// It reads nothing from the database and touches no game: the bundle is
// embedded in the binary and is the same for every game on the instance.
// It is registered through addScopedTool all the same, because that is
// the one registration path this server's own test walks the served tool
// list against — a tool that skipped it to save an admission check it
// does not need would be a tool nothing proves goes through the check.
func MCPSkillInstall(ctx context.Context, s *Server) SkillInstallOutput {
	expiry := time.Now().Add(skillURLTTL)
	return SkillInstallOutput{
		DownloadURL:   signedSkillURL(s.skillURLKey, externalBaseURLFrom(ctx), expiry.Unix()),
		ExpiresAt:     expiry.UTC().Format(time.RFC3339),
		Format:        "zip",
		BundleVersion: skill.Version(),
		Install: SkillInstallTarget{
			Name:         "maestro",
			PreferredDir: "<workspace>/.claude/skills/maestro",
			FallbackDir:  "~/.claude/skills/maestro",
			Instructions: skillInstallInstructions,
		},
	}
}

// addSkillTools registers skill.install.
func (s *Server) addSkillTools(srv *mcp.Server, deps MCPDeps) {
	addScopedTool(s, srv, deps, &mcp.Tool{
		Name: "skill.install",
		Description: "Get the Maestro skill bundle: the pages that teach how to declare a " +
			"game's own vocabulary, seed content and compose views. Returns a short-lived " +
			"`download_url`, a `bundle_version`, and where to put the files — **not the " +
			"files themselves**, deliberately: the bundle costs about twenty thousand " +
			"tokens and inlining it here would charge you that on every call, including " +
			"the calls where you already had it.\n\n" +
			"Fetch the zip with your own HTTP tool, extract it over the install " +
			"directory, and write `bundle_version` into a manifest beside it; a matching " +
			"version next time means skip the download. The URL is signed and expires in " +
			"five minutes, and needs no Authorization header.\n\n" +
			"**Tell the human to restart the application that loads you.** Most agent " +
			"runtimes read a skills directory once, at session start; until then the old " +
			"pages are the ones in effect, and the human cannot infer that from your " +
			"output.",
		OutputSchema: skillInstallOutputSchema,
		Annotations:  readOnlyTool(),
	}, func(ctx context.Context, _ MCPDeps, _ uuid.UUID, _ skillInstallInput) (SkillInstallOutput, error) {
		return MCPSkillInstall(ctx, s), nil
	})
}

var skillInstallOutputSchema = &jsonschema.Schema{
	Type:     "object",
	Required: []string{"download_url", "expires_at", "format", "bundle_version", "install"},
	Properties: map[string]*jsonschema.Schema{
		"download_url":   stringSchema(),
		"expires_at":     stringSchema(),
		"format":         stringSchema(),
		"bundle_version": stringSchema(),
		"install": {
			Type:     "object",
			Required: []string{"name", "preferred_dir", "fallback_dir", "instructions"},
			Properties: map[string]*jsonschema.Schema{
				"name":          stringSchema(),
				"preferred_dir": stringSchema(),
				"fallback_dir":  stringSchema(),
				"instructions":  stringSchema(),
			},
		},
	},
}
