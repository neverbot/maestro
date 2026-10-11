package web_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/markdown"
	"github.com/neverbot/maestro/internal/web"
)

// A document written from a file over a signed URL: the whole point is
// that the bytes never pass through a tool call, so the whole of this
// is driven the way an agent drives it — mint over MCP, then send the
// file with no credential at all.

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// put sends a body to a signed URL carrying nothing else: no cookie, no
// bearer token. That is the claim being tested.
func put(t *testing.T, f restFixture, signed, body string) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := url.Parse(signed)
	assert.Must(t, err == nil, "the minted url does not parse: %v", err)
	req := httptest.NewRequest(http.MethodPut, parsed.RequestURI(), strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func mint(t *testing.T, f restFixture, in web.DocsUploadURLInput) web.DocsUploadURLOutput {
	t.Helper()
	out, err := web.MCPDocsUploadURL(t.Context(), f.deps(), f.caller(), f.game, in)
	assert.Must(t, err == nil, "docs.upload_url: %v", err)
	return out
}

func version(n int32) *int32 { return &n }

// TestADocumentIsUploadedFromAFileWithNoCredential is the round trip.
func TestADocumentIsUploadedFromAFileWithNoCredential(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	const body = "---\nkind: lore\ntitle: The Deep Road\n---\n\nIt goes down, and then it goes further down.\n"

	kind := "lore"
	minted := mint(t, f, web.DocsUploadURLInput{
		Path: "lore/deep-road.md", ExpectedVersion: version(0),
		SHA256: digestOf(body), Message: "first draft", Kind: &kind,
	})
	assert.Must(t, minted.UploadURL != "", "no url was minted")
	assert.Should(t, minted.MaxBytes == markdown.MaxBodyBytes, "max_bytes is %d", minted.MaxBytes)
	// **Five minutes, not the image download's hour.** The two are
	// fetched on different clocks: an agent mints this and sends the
	// file in the same breath, while an image URL is read out of an
	// entity's answer and acted on later. The expiry itself is enforced
	// by the shared verifier, which TestAnExpiredSkillURLIsRefused
	// pins; what is checked here is that this route asks for the short
	// one.
	assert.Should(t, minted.ExpiresInSeconds == 300,
		"an upload url is good for %d seconds, want 300", minted.ExpiresInSeconds)

	rec := put(t, f, minted.UploadURL, body)
	assert.Must(t, rec.Code == http.StatusOK, "upload = %d: %s", rec.Code, rec.Body.String())

	var ack web.DocUploadedOutput
	assert.Must(t, json.Unmarshal(rec.Body.Bytes(), &ack) == nil, "decode the ack")
	assert.Should(t, ack.Path == "lore/deep-road.md", "path %q", ack.Path)
	assert.Should(t, ack.Version == 1, "version %d, want 1", ack.Version)
	// **The frontmatter was parsed by the same core docs.write uses**,
	// which is the whole reason this route builds a markdown.WriteInput
	// instead of writing its own.
	assert.Should(t, ack.Title == "The Deep Road", "title %q: the frontmatter was not read", ack.Title)
	// **kind and message are fixed when the URL is minted**, not when
	// the file is sent: they are signed arguments, so the upload cannot
	// change what it is filed under. This product does not read kind out
	// of the frontmatter, which is why it is an argument at all.
	assert.Should(t, ack.Kind == "lore", "kind %q: the kind signed into the url did not reach the row", ack.Kind)
	// And the body is not echoed back, which is the cost this route
	// exists to avoid, paid in reverse.
	assert.Should(t, !strings.Contains(rec.Body.String(), "goes further down"),
		"the upload answered with the body it was just sent")

	// The document really is there, read the ordinary way.
	read, err := web.MCPDocsRead(t.Context(), f.deps(), f.caller(), f.game,
		web.DocsReadInput{Path: "lore/deep-road.md"})
	assert.Must(t, err == nil, "docs.read: %v", err)
	assert.Should(t, strings.Contains(read.Body, "goes further down"), "the body did not land: %q", read.Body)
}

// TestAnUploadURLWritesThoseBytesAndNoOthers is what the digest buys. A
// URL that leaks is a URL that can write the content it was minted for
// and nothing else.
func TestAnUploadURLWritesThoseBytesAndNoOthers(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	const intended = "# The agreed text\n"
	const substituted = "# Something else entirely\n"

	minted := mint(t, f, web.DocsUploadURLInput{
		Path: "lore/agreed.md", ExpectedVersion: version(0), SHA256: digestOf(intended),
	})
	rec := put(t, f, minted.UploadURL, substituted)
	assert.Must(t, rec.Code == http.StatusBadRequest, "substituted bytes answered %d, want 400", rec.Code)
	assert.Should(t, strings.Contains(rec.Body.String(), "nothing was written"),
		"the refusal does not say the write did not happen: %s", rec.Body.String())

	// And nothing was written.
	_, err := web.MCPDocsRead(t.Context(), f.deps(), f.caller(), f.game,
		web.DocsReadInput{Path: "lore/agreed.md"})
	assert.Should(t, err != nil, "the document exists after a refused upload")

	// The intended bytes still land on the same URL.
	rec = put(t, f, minted.UploadURL, intended)
	assert.Should(t, rec.Code == http.StatusOK, "the intended bytes answered %d: %s", rec.Code, rec.Body.String())
}

// TestAnUploadURLIsGoodForOneWrite pins the single use, which nothing
// stores: the version it names is the one it replaces, so the write
// that lands moves the document past it.
func TestAnUploadURLIsGoodForOneWrite(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	const body = "# Once\n"

	minted := mint(t, f, web.DocsUploadURLInput{
		Path: "lore/once.md", ExpectedVersion: version(0), SHA256: digestOf(body),
	})
	assert.Must(t, put(t, f, minted.UploadURL, body).Code == http.StatusOK, "the first upload")

	again := put(t, f, minted.UploadURL, body)
	assert.Should(t, again.Code == http.StatusConflict,
		"the same url wrote twice, answering %d: a ticket that can be replayed is a ticket that overwrites "+
			"whatever came after it", again.Code)
	assert.Should(t, strings.Contains(again.Body.String(), "version_conflict"),
		"the second write did not answer version_conflict: %s", again.Body.String())
}

// TestAnEditedUploadURLIsRefused is the signature doing its work: every
// argument is covered, so none of them can be changed after the fact.
func TestAnEditedUploadURLIsRefused(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)
	const body = "# Signed\n"
	minted := mint(t, f, web.DocsUploadURLInput{
		Path: "lore/signed.md", ExpectedVersion: version(0), SHA256: digestOf(body),
	})

	for _, edit := range []struct {
		name string
		was  string
		now  string
	}{
		{"the document it writes", "path=lore%2Fsigned.md", "path=lore%2Fsomebody-elses.md"},
		{"the version it replaces", "version=0", "version=3"},
		{"the bytes it admits", "sha256=" + digestOf(body), "sha256=" + digestOf("# Other\n")},
	} {
		t.Run(edit.name, func(t *testing.T) {
			tampered := strings.Replace(minted.UploadURL, edit.was, edit.now, 1)
			assert.Must(t, tampered != minted.UploadURL, "the edit %q matched nothing in %s", edit.was, minted.UploadURL)
			rec := put(t, f, tampered, body)
			assert.Should(t, rec.Code == http.StatusUnauthorized,
				"editing %s answered %d, want 401", edit.name, rec.Code)
		})
	}

	// An unsigned request, for the floor.
	bare := strings.Split(minted.UploadURL, "?")[0]
	assert.Should(t, put(t, f, bare, body).Code == http.StatusUnauthorized,
		"an unsigned PUT was admitted")
}

// TestAnUploadIsBoundedAndAttributed covers the two things the route
// owes the rest of the product: the same ceiling docs.write has, and a
// version row that names who wrote it.
func TestAnUploadIsBoundedAndAttributed(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	over := strings.Repeat("x", markdown.MaxBodyBytes+1)
	minted := mint(t, f, web.DocsUploadURLInput{
		Path: "lore/huge.md", ExpectedVersion: version(0), SHA256: digestOf(over),
	})
	rec := put(t, f, minted.UploadURL, over)
	assert.Should(t, rec.Code == http.StatusBadRequest, "an over-long body answered %d, want 400", rec.Code)

	// **The author is the agent that minted the URL**, carried in the
	// signature because the upload itself has no caller to attribute.
	const body = "# Attributed\n"
	minted = mint(t, f, web.DocsUploadURLInput{
		Path: "lore/attributed.md", ExpectedVersion: version(0), SHA256: digestOf(body),
	})
	assert.Must(t, put(t, f, minted.UploadURL, body).Code == http.StatusOK, "upload")

	history, err := web.MCPDocsHistory(t.Context(), f.deps(), f.caller(), f.game,
		web.DocsHistoryInput{Path: "lore/attributed.md"})
	assert.Must(t, err == nil, "docs.history: %v", err)
	assert.Must(t, len(history.Items) == 1, "%d versions", len(history.Items))
	assert.Should(t, history.Items[0].AuthorKind == "token",
		"the version says %q wrote it, want the agent whose token minted the url", history.Items[0].AuthorKind)
}

// TestTheMintingCallRefusesWhatTheUploadCannotFix checks the arguments
// where a refusal is worth a sentence an agent reads, rather than an
// HTTP status arriving in a shell after the bytes went up the wire.
func TestTheMintingCallRefusesWhatTheUploadCannotFix(t *testing.T) {
	t.Parallel()
	f := newRESTFixture(t)

	for _, bad := range []struct {
		name string
		in   web.DocsUploadURLInput
	}{
		{"no version at all", web.DocsUploadURLInput{Path: "a.md", SHA256: digestOf("x")}},
		{"no digest", web.DocsUploadURLInput{Path: "a.md", ExpectedVersion: version(0)}},
		{"a digest of the wrong length", web.DocsUploadURLInput{
			Path: "a.md", ExpectedVersion: version(0), SHA256: "abc"}},
		{"a digest that is not hexadecimal", web.DocsUploadURLInput{
			Path: "a.md", ExpectedVersion: version(0), SHA256: strings.Repeat("z", 64)}},
		{"no path", web.DocsUploadURLInput{ExpectedVersion: version(0), SHA256: digestOf("x")}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			_, err := web.MCPDocsUploadURL(t.Context(), f.deps(), f.caller(), f.game, bad.in)
			assert.Should(t, err != nil, "minting was allowed with %s", bad.name)
		})
	}
}
