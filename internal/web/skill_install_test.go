package web_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/web"
)

// The delivery half of the skill bundle: a signed, short-lived
// /skill.zip and the descriptor that points at it.
func newSkillServer(t *testing.T) *web.Server {
	t.Helper()
	return web.NewServer(web.Options{
		Version:  "skill-test",
		Identity: identity.New(nil, config.Config{}),
		Projects: projects.New(nil),
	})
}

// readAll drains a recorded response body.
func readAll(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	assert.Must(t, err == nil, "reading the response body: %v", err)
	return body
}

// get issues one request against a server and returns the response.
func get(t *testing.T, srv *web.Server, url string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder.Result()
}

// TestASignedSkillURLServesTheBundle is the control every refusal below
// rests on. Without it, a handler that refused every request in the
// world would pass the whole rest of this file.
func TestASignedSkillURLServesTheBundle(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, srv.SignedSkillURLForTest("", time.Now().Add(time.Minute).Unix()))
	assert.Must(t, response.StatusCode == http.StatusOK, "a live signed URL answered %d, want 200", response.StatusCode)
	if got := response.Header.Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", got)
	}
	if got := response.Header.Get("ETag"); !strings.Contains(got, skill.Version()) {
		t.Errorf("ETag = %q, want it to carry the bundle version %q", got, skill.Version())
	}
}

// TestASkillURLWithoutASignatureIsRefused: the bare path is not a way in.
func TestASkillURLWithoutASignatureIsRefused(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, web.SkillZipPathForTest())
	assert.Must(t, response.StatusCode == http.StatusUnauthorized, "an unsigned URL answered %d, want 401", response.StatusCode)
	body := readAll(t, response)
	assert.Should(t, len(body) == 0, "a refusal carried a %d-byte body: %q", len(body), body)
}

// TestASkillURLSignedForAnotherPathIsRefused presents a signature that
// is perfectly valid — for a different path.
func TestASkillURLSignedForAnotherPathIsRefused(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	exp := time.Now().Add(time.Minute).Unix()
	elsewhere := srv.SkillURLSignatureForTest("/some/other/download", exp)
	assert.Must(t, elsewhere != srv.SkillURLSignatureForTest(web.SkillZipPathForTest(), exp), "two paths signed to the same value: the path is not inside the MAC at all")
	response := get(t, srv, web.SkillZipPathForTest()+
		"?exp="+strconv.FormatInt(exp, 10)+"&sig="+elsewhere)
	assert.Must(t, response.StatusCode == http.StatusUnauthorized, "a signature minted for another path answered %d, want 401", response.StatusCode)
}

// TestAnExpiredSkillURLIsRefused.
func TestAnExpiredSkillURLIsRefused(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, srv.SignedSkillURLForTest("", time.Now().Add(-time.Second).Unix()))
	assert.Must(t, response.StatusCode == http.StatusUnauthorized, "an expired URL answered %d, want 401", response.StatusCode)
}

// TestASkillURLFromAnotherProcessIsRefused signs with a second server's
// key and presents it to the first.
func TestASkillURLFromAnotherProcessIsRefused(t *testing.T) {
	t.Parallel()
	first, second := newSkillServer(t), newSkillServer(t)
	exp := time.Now().Add(time.Minute).Unix()
	url := second.SignedSkillURLForTest("", exp)
	if url == first.SignedSkillURLForTest("", exp) {
		t.Fatal("two servers signed one URL identically: the signing key is shared between " +
			"processes, which is a constant wearing a secret's costume")
	}
	if response := get(t, first, url); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a URL signed by another process answered %d, want 401", response.StatusCode)
	}
	// And the control: that same URL works against the server that minted
	// it, so the refusal above is about the key and not about the URL
	// being malformed.
	if response := get(t, second, url); response.StatusCode != http.StatusOK {
		t.Fatalf("the minting server refused its own URL with %d", response.StatusCode)
	}
}

// TestTheSkillZipIsTheEmbeddedBundle reads the served bytes back and
// compares them, entry by entry, against skill.Files().
func TestTheSkillZipIsTheEmbeddedBundle(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, srv.SignedSkillURLForTest("", time.Now().Add(time.Minute).Unix()))
	assert.Must(t, response.StatusCode == http.StatusOK, "the signed URL answered %d, want 200", response.StatusCode)
	served := readAll(t, response)

	packed, err := skill.Zip()
	assert.Must(t, err == nil, "packing the bundle: %v", err)
	assert.Must(t, bytes.Equal(served, packed), "the served archive is %d bytes and skill.Zip() is %d: the route packs "+
		"something other than the embedded bundle", len(served), len(packed))

	archive, err := zip.NewReader(bytes.NewReader(served), int64(len(served)))
	assert.Must(t, err == nil, "reading the served archive: %v", err)
	inZip := map[string]bool{}
	for _, entry := range archive.File {
		inZip[entry.Name] = true
	}

	var want []string
	if err := fs.WalkDir(skill.Files(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		want = append(want, path)
		return nil
	}); err != nil {
		t.Fatalf("walking the bundle: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the bundle walk found no files: the comparison below would run over an " +
			"empty set and pass against an empty archive")
	}
	sort.Strings(want)
	var missing []string
	for _, path := range want {
		if !inZip[path] {
			missing = append(missing, path)
		}
	}
	assert.Should(t, len(missing) <= 0, "the served archive is missing %d of the bundle's %d files:\n%s",
		len(missing), len(want), strings.Join(missing, "\n"))
	// The pages, plus the manifest an installed copy checks itself with.
	assert.Should(t, inZip[skill.ManifestName], "the served archive carries no %s, so an installed copy cannot check itself", skill.ManifestName)
	assert.Should(t, len(inZip) == len(want)+1, "the archive holds %d entries and the bundle holds %d files plus its manifest", len(inZip), len(want))
}

// TestTheInstallDescriptorCarriesNoBundleBytes is the whole reason this
// mechanism exists, and the one property of it nothing else asserts.
func TestTheInstallDescriptorCarriesNoBundleBytes(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	descriptor := web.MCPSkillInstall(context.Background(), srv)
	encoded, err := json.Marshal(descriptor)
	assert.Must(t, err == nil, "marshalling the descriptor: %v", err)
	assert.Should(t, len(encoded) <= 2048, "the install descriptor marshals to %d bytes, over the 2 KB this mechanism "+
		"exists to stay under:\n%s", len(encoded), encoded)
	if bytes.Contains(encoded, []byte("PK\x03\x04")) {
		t.Error("the install descriptor carries a zip's local file header: the bundle bytes " +
			"are travelling through the MCP response, which is the one thing download_url " +
			"exists to prevent")
	}
	assert.Should(t, descriptor.BundleVersion == skill.Version(), "bundle_version = %q, want the bundle's own hash %q",
		descriptor.BundleVersion, skill.Version())
	assert.Should(t, strings.Contains(descriptor.DownloadURL, web.SkillZipPathForTest()), "download_url %q does not point at the download route", descriptor.DownloadURL)
}

// TestTheInstallDescriptorURLIsFetchable closes the loop between the two
// halves of this file: the URL the descriptor hands out is one the
// handler admits.
func TestTheInstallDescriptorURLIsFetchable(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	descriptor := web.MCPSkillInstall(context.Background(), srv)
	if response := get(t, srv, descriptor.DownloadURL); response.StatusCode != http.StatusOK {
		t.Fatalf("the URL skill.install handed out answered %d, want 200", response.StatusCode)
	}
	expires, err := time.Parse(time.RFC3339, descriptor.ExpiresAt)
	assert.Must(t, err == nil, "expires_at %q is not RFC3339: %v", descriptor.ExpiresAt, err)
	assert.Should(t, time.Until(expires) > 0, "expires_at %q is already past", descriptor.ExpiresAt)
}

// TestWhoamiReportsTheVersionSkillInstallServes is the version
// handshake, read through one session over the real transport.
func TestWhoamiReportsTheVersionSkillInstallServes(t *testing.T) {
	t.Parallel()
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "handshake@example.test", DisplayName: "Owner", Password: "password12345"})
	assert.Must(t, err == nil, "CreateUser: %v", err)
	game, err := projSvc.Create(ctx, "handshake", "Handshake", user.ID)
	assert.Must(t, err == nil, "Create: %v", err)
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent"})
	assert.Must(t, err == nil, "CreateAPIToken: %v", err)

	session := connectMCP(t, httpSrv.URL, token)

	var who struct {
		SkillBundleVersion string `json:"skill_bundle_version"`
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	assert.Must(t, err == nil, "CallTool(whoami): %v", err)
	decodeStructured(t, result, &who)

	var install struct {
		BundleVersion string `json:"bundle_version"`
		DownloadURL   string `json:"download_url"`
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "skill.install"})
	assert.Must(t, err == nil, "CallTool(skill.install): %v", err)
	assert.Must(t, !result.IsError, "skill.install reported an error: %+v", result.Content)
	decodeStructured(t, result, &install)

	assert.Must(t, who.SkillBundleVersion != "" && install.BundleVersion != "", "one of the two versions is empty (whoami %q, skill.install %q): a "+
		"comparison of two empty strings passes against anything",
		who.SkillBundleVersion, install.BundleVersion)
	assert.Must(t, who.SkillBundleVersion == install.BundleVersion, "whoami reports skill_bundle_version %q and skill.install reports "+
		"bundle_version %q: two fields carrying one fact have two sources",
		who.SkillBundleVersion, install.BundleVersion)

	// And the URL an agent actually receives over the transport is
	// absolute, so it can be fetched without guessing a host. This is the
	// half that a direct call to MCPSkillInstall cannot see, since
	// nothing stashes an origin on a context a test built itself.
	assert.Should(t, strings.HasPrefix(install.DownloadURL, httpSrv.URL+web.SkillZipPathForTest()), "download_url over the transport is %q, want it rooted at %q",
		install.DownloadURL, httpSrv.URL+web.SkillZipPathForTest())
	response, err := http.Get(install.DownloadURL) //nolint:gosec,noctx // the URL is this test's own server, and the fetch carries no header by design.
	assert.Must(t, err == nil, "fetching the download URL: %v", err)
	defer func() { _ = response.Body.Close() }()
	assert.Must(t, response.StatusCode == http.StatusOK, "fetching the URL an agent was handed answered %d, want 200", response.StatusCode)
}
