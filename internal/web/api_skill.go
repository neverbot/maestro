package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neverbot/maestro/internal/skill"
)

// This file serves the skill bundle as a zip over one unauthenticated,
// signed, short-lived URL.
//
// **The bundle bytes never travel through an MCP response.** skill.install
// (mcp_skill.go) hands out a descriptor pointing here; the agent fetches
// the archive with whatever HTTP tool the host gives it. A 20k-token
// bundle inlined into a tool result would cost its full price on every
// call, including the calls where the agent already had it.
//
// **The URL carries its own authorisation and takes no bearer header.**
// The alternative — the ordinary Authorization header — puts a game token
// into whatever `curl` invocation the agent constructs, which is a token
// in a shell command in a transcript. The signature is the price of
// keeping it out of one.

// skillZipPath is the download route, named once so the handler, the
// signer and the descriptor cannot disagree about which path is signed.
const skillZipPath = "/skill.zip"

// skillURLTTL is how long a signed download URL stays good. Long enough
// to absorb a retry on a slow link, short enough that a URL which leaks
// into a transcript is worthless by the time anybody reads it.
const skillURLTTL = 5 * time.Minute

// signSkillURL returns the `exp` and `sig` query arguments that admit a
// request to path until exp.
//
// **The path is inside the MAC**, not merely beside it. A signature that
// covered only the expiry would be a signature valid on every route that
// ever learns to check one, so the day a second signed download exists
// the first one's URLs would open it.
func signSkillURL(key []byte, path string, exp int64) string {
	mac := hmac.New(sha256.New, key)
	// The separator is a byte that cannot occur in a URL path, so
	// ("/skill.zip", 12) and ("/skill.zi", "p12") cannot produce one
	// message. It is the same length-confusion this repository's bundle
	// hash length-prefixes its entries to avoid.
	mac.Write([]byte(path))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// signedSkillURL builds the whole download URL: base (which may be empty,
// giving a path-only URL the agent resolves against the server it is
// already talking to) plus the path, the expiry and the signature.
func signedSkillURL(key []byte, base string, exp int64) string {
	return strings.TrimSuffix(base, "/") + skillZipPath +
		"?exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + signSkillURL(key, skillZipPath, exp)
}

// handleSkillZip serves the embedded bundle to a caller holding a live
// signature, and refuses everything else with 401 and no body.
//
// No bearer header is read here and none is accepted: this route's whole
// reason to exist is that the agent's token stays out of the fetch. A
// caller without a signature is refused rather than served, even though
// the bundle is not secret — an unauthenticated download of an archive
// that grows with the product is a free amplifier, and "it is not secret"
// is an argument about confidentiality, not about who gets to spend the
// bandwidth.
func (s *Server) handleSkillZip(w http.ResponseWriter, r *http.Request) {
	if !s.skillURLIsLive(r) {
		// No body. There is nothing a caller can do with a description of
		// why a signature failed that they cannot do by asking
		// skill.install for a fresh URL.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	archive, err := skill.Zip()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errCodeInternal,
			"the skill bundle could not be packed")
		return
	}
	// The ETag is the bundle's content hash, which is the same value
	// skill.install reports as bundle_version and whoami reports as
	// skill_bundle_version. One fact, one source: an agent that stored
	// the version can compare it against the header without unpacking
	// anything.
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("ETag", strconv.Quote(skill.Version()))
	w.Header().Set("Content-Disposition", `attachment; filename="maestro-skill.zip"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	_, _ = w.Write(archive)
}

// skillURLIsLive reports whether this request carries a signature this
// process minted, for this path, that has not expired.
func (s *Server) skillURLIsLive(r *http.Request) bool {
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	sig := r.URL.Query().Get("sig")
	want := signSkillURL(s.skillURLKey, r.URL.Path, exp)
	// Constant-time, over the hex text rather than the decoded bytes: a
	// malformed hex signature is unequal either way, and this arm never
	// has to decide what a decode error means.
	return hmac.Equal([]byte(want), []byte(sig))
}

// externalBaseURL is the scheme and host an agent used to reach this
// server, stashed on the request context by mcpHandler so a tool handler
// — which is handed a context and never an *http.Request — can build an
// absolute download URL.
type externalBaseURLKey struct{}

func withExternalBaseURL(ctx context.Context, base string) context.Context {
	if base == "" {
		return ctx
	}
	return context.WithValue(ctx, externalBaseURLKey{}, base)
}

// externalBaseURLFrom returns the stashed base, or "" when there is none.
// **An empty base is not an error.** MCPSkillInstall is called directly
// by tests and could one day be called over a transport that has no HTTP
// request behind it at all; a path-only download_url resolves against the
// server the agent is already talking to, which is the right answer in
// every case a base is missing.
func externalBaseURLFrom(ctx context.Context) string {
	base, _ := ctx.Value(externalBaseURLKey{}).(string)
	return base
}

// externalBaseURLFor reconstructs the origin a request arrived on.
//
// X-Forwarded-Proto and X-Forwarded-Host are read **only behind a trusted
// proxy**, on the same operator decision clientIP and the session cookie
// already gate on: on a directly exposed instance both headers are
// attacker-controlled, and this one is used to build a URL the caller is
// then told to fetch.
func (s *Server) externalBaseURLFor(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if s.behindTrustedProxy() {
		if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
			scheme = forwarded
		}
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host = forwarded
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
