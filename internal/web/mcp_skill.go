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
type SkillInstallTarget struct {
	Name         string `json:"name"`
	PreferredDir string `json:"preferred_dir"`
	FallbackDir  string `json:"fallback_dir"`
	Instructions string `json:"instructions"`
}

// SkillInstallOutput is the descriptor skill.install answers with.
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
