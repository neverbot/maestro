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

	"github.com/neverbot/maestro/internal/config"
	"github.com/neverbot/maestro/internal/identity"
	"github.com/neverbot/maestro/internal/projects"
	"github.com/neverbot/maestro/internal/skill"
	"github.com/neverbot/maestro/internal/web"
)

// The delivery half of the skill bundle: a signed, short-lived
// /skill.zip and the descriptor that points at it.
//
// Every test below except the last builds its server over a nil pool.
// None of them executes a query — signing a URL, packing an embedded
// tree and marshalling a descriptor all touch the binary and nothing
// else — so they run without a database and stay in the fast loop this
// sub-project is written in.

// newSkillServer builds a server that can sign and serve the bundle and
// nothing else. Identity and Projects are required by NewServer and are
// never called.
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
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
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
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a live signed URL answered %d, want 200", response.StatusCode)
	}
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
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unsigned URL answered %d, want 401", response.StatusCode)
	}
	body := readAll(t, response)
	if len(body) != 0 {
		t.Errorf("a refusal carried a %d-byte body: %q", len(body), body)
	}
}

// TestASkillURLSignedForAnotherPathIsRefused presents a signature that
// is perfectly valid — for a different path.
//
// The signature covers the path for a reason, and this is that reason: a
// MAC over the expiry alone would be a MAC that any second signed route
// this server ever grows would also accept.
func TestASkillURLSignedForAnotherPathIsRefused(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	exp := time.Now().Add(time.Minute).Unix()
	elsewhere := srv.SkillURLSignatureForTest("/some/other/download", exp)
	if elsewhere == srv.SkillURLSignatureForTest(web.SkillZipPathForTest(), exp) {
		t.Fatal("two paths signed to the same value: the path is not inside the MAC at all")
	}
	response := get(t, srv, web.SkillZipPathForTest()+
		"?exp="+strconv.FormatInt(exp, 10)+"&sig="+elsewhere)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a signature minted for another path answered %d, want 401", response.StatusCode)
	}
}

// TestAnExpiredSkillURLIsRefused.
func TestAnExpiredSkillURLIsRefused(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, srv.SignedSkillURLForTest("", time.Now().Add(-time.Second).Unix()))
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired URL answered %d, want 401", response.StatusCode)
	}
}

// TestASkillURLFromAnotherProcessIsRefused signs with a second server's
// key and presents it to the first.
//
// This is the assertion that the key is per-process rather than a
// constant somebody hardcoded "temporarily", and it is the one test in
// this file that a fixed key would fail while every other test in it
// stayed green.
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
//
// **Mutation, run while writing this test:** drop the genres/ prefix
// from bundlePaths, so the zip builder packs everything else. This test
// fails naming the six missing paths; nothing else in this file notices,
// because a descriptor pointing at an archive is a descriptor that
// cannot tell what is in it.
func TestTheSkillZipIsTheEmbeddedBundle(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	response := get(t, srv, srv.SignedSkillURLForTest("", time.Now().Add(time.Minute).Unix()))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the signed URL answered %d, want 200", response.StatusCode)
	}
	served := readAll(t, response)

	packed, err := skill.Zip()
	if err != nil {
		t.Fatalf("packing the bundle: %v", err)
	}
	if !bytes.Equal(served, packed) {
		t.Fatalf("the served archive is %d bytes and skill.Zip() is %d: the route packs "+
			"something other than the embedded bundle", len(served), len(packed))
	}

	archive, err := zip.NewReader(bytes.NewReader(served), int64(len(served)))
	if err != nil {
		t.Fatalf("reading the served archive: %v", err)
	}
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
	if len(missing) > 0 {
		t.Errorf("the served archive is missing %d of the bundle's %d files:\n%s",
			len(missing), len(want), strings.Join(missing, "\n"))
	}
	if len(inZip) != len(want) {
		t.Errorf("the archive holds %d entries and the bundle holds %d files", len(inZip), len(want))
	}
}

// TestTheInstallDescriptorCarriesNoBundleBytes is the whole reason this
// mechanism exists, and the one property of it nothing else asserts.
//
// A descriptor that quietly grew an `files` member holding the archive
// would pass every other test in this file: the URL would still be
// signed, the zip would still be the bundle, and the agent would pay
// twenty thousand tokens on every call.
func TestTheInstallDescriptorCarriesNoBundleBytes(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	descriptor := web.MCPSkillInstall(context.Background(), srv)
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshalling the descriptor: %v", err)
	}
	if len(encoded) > 2048 {
		t.Errorf("the install descriptor marshals to %d bytes, over the 2 KB this mechanism "+
			"exists to stay under:\n%s", len(encoded), encoded)
	}
	if bytes.Contains(encoded, []byte("PK\x03\x04")) {
		t.Error("the install descriptor carries a zip's local file header: the bundle bytes " +
			"are travelling through the MCP response, which is the one thing download_url " +
			"exists to prevent")
	}
	if descriptor.BundleVersion != skill.Version() {
		t.Errorf("bundle_version = %q, want the bundle's own hash %q",
			descriptor.BundleVersion, skill.Version())
	}
	if !strings.Contains(descriptor.DownloadURL, web.SkillZipPathForTest()) {
		t.Errorf("download_url %q does not point at the download route", descriptor.DownloadURL)
	}
}

// TestTheInstallDescriptorURLIsFetchable closes the loop between the two
// halves of this file: the URL the descriptor hands out is one the
// handler admits.
//
// Without it, the descriptor could sign one path while the route checked
// another and every test above would still be green — each half correct
// about itself and the pair useless.
func TestTheInstallDescriptorURLIsFetchable(t *testing.T) {
	t.Parallel()
	srv := newSkillServer(t)
	descriptor := web.MCPSkillInstall(context.Background(), srv)
	if response := get(t, srv, descriptor.DownloadURL); response.StatusCode != http.StatusOK {
		t.Fatalf("the URL skill.install handed out answered %d, want 200", response.StatusCode)
	}
	expires, err := time.Parse(time.RFC3339, descriptor.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", descriptor.ExpiresAt, err)
	}
	if time.Until(expires) <= 0 {
		t.Errorf("expires_at %q is already past", descriptor.ExpiresAt)
	}
}

// TestWhoamiReportsTheVersionSkillInstallServes is the version
// handshake, read through one session over the real transport.
//
// **Mutation, run while writing this test:** hardcode
// WhoamiOutput.SkillBundleVersion to "sha256:0". This test fails naming
// both values; nothing else in the repository does, because each half is
// internally consistent and only the comparison between them is the
// handshake. Two fields carrying one fact from one source is the whole
// mechanism; two fields carrying one fact from two sources is the defect
// this repository spent a sub-project removing.
func TestWhoamiReportsTheVersionSkillInstallServes(t *testing.T) {
	srv, ids, projSvc := newTestServer(t)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	ctx := context.Background()

	user, err := ids.CreateUser(ctx, identity.CreateUserRequest{
		Email: "handshake@studio.com", DisplayName: "Owner", Password: "password12345"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	game, err := projSvc.Create(ctx, "handshake", "Handshake", user.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	token, _, err := ids.CreateAPIToken(ctx, identity.CreateAPITokenRequest{
		ProjectID: game.ID, UserID: user.ID, Label: "agent"})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	session := connectMCP(t, httpSrv.URL, token)

	var who struct {
		SkillBundleVersion string `json:"skill_bundle_version"`
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("CallTool(whoami): %v", err)
	}
	decodeStructured(t, result, &who)

	var install struct {
		BundleVersion string `json:"bundle_version"`
		DownloadURL   string `json:"download_url"`
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "skill.install"})
	if err != nil {
		t.Fatalf("CallTool(skill.install): %v", err)
	}
	if result.IsError {
		t.Fatalf("skill.install reported an error: %+v", result.Content)
	}
	decodeStructured(t, result, &install)

	if who.SkillBundleVersion == "" || install.BundleVersion == "" {
		t.Fatalf("one of the two versions is empty (whoami %q, skill.install %q): a "+
			"comparison of two empty strings passes against anything",
			who.SkillBundleVersion, install.BundleVersion)
	}
	if who.SkillBundleVersion != install.BundleVersion {
		t.Fatalf("whoami reports skill_bundle_version %q and skill.install reports "+
			"bundle_version %q: two fields carrying one fact have two sources",
			who.SkillBundleVersion, install.BundleVersion)
	}

	// And the URL an agent actually receives over the transport is
	// absolute, so it can be fetched without guessing a host. This is the
	// half that a direct call to MCPSkillInstall cannot see, since
	// nothing stashes an origin on a context a test built itself.
	if !strings.HasPrefix(install.DownloadURL, httpSrv.URL+web.SkillZipPathForTest()) {
		t.Errorf("download_url over the transport is %q, want it rooted at %q",
			install.DownloadURL, httpSrv.URL+web.SkillZipPathForTest())
	}
	response, err := http.Get(install.DownloadURL) //nolint:gosec,noctx // the URL is this test's own server, and the fetch carries no header by design.
	if err != nil {
		t.Fatalf("fetching the download URL: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fetching the URL an agent was handed answered %d, want 200", response.StatusCode)
	}
}
