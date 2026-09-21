package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The backup loop's own checks.
//
// **Every one of these is a property that cost something to learn**, and
// they came across for that reason rather than for coverage: the dump
// lands 0600, an existing directory is tightened, the password never
// reaches the process arguments, retention matches this package's own
// filenames and nothing else, and an orphan .tmp from an interrupted
// dump is swept after a day.
//
// dumpOnce is driven against a stand-in pg_dump placed first on PATH
// (see fakePgDump): what matters here is how the dump file is created
// and how the command is invoked, not what Postgres puts inside it.

func TestParseClock(t *testing.T) {
	cases := []struct {
		in       string
		wantH    int
		wantM    int
		wantErr  bool
		errLabel string
	}{
		{in: "03:00", wantH: 3, wantM: 0},
		{in: "00:00", wantH: 0, wantM: 0},
		{in: "23:59", wantH: 23, wantM: 59},
		{in: "9:05", wantH: 9, wantM: 5},
		{in: "", wantErr: true, errLabel: "empty"},
		{in: "0300", wantErr: true, errLabel: "no colon"},
		{in: "ab:cd", wantErr: true, errLabel: "non-numeric"},
		{in: "24:00", wantErr: true, errLabel: "H out of range"},
		{in: "12:60", wantErr: true, errLabel: "M out of range"},
		{in: "1:2:3", wantErr: true, errLabel: "too many parts"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			h, m, err := parseClock(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q (%s), got h=%d m=%d", tc.in, tc.errLabel, h, m)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClock(%q): %v", tc.in, err)
			}
			if h != tc.wantH || m != tc.wantM {
				t.Errorf("parseClock(%q) = (%d,%d), want (%d,%d)", tc.in, h, m, tc.wantH, tc.wantM)
			}
		})
	}
}

func TestNextFire_LaterToday(t *testing.T) {
	now := time.Date(2026, 6, 7, 1, 30, 0, 0, time.Local)
	got := nextFire(now, 3, 0)
	want := time.Date(2026, 6, 7, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("nextFire(01:30, 03:00) = %v, want %v", got, want)
	}
}

func TestNextFire_EarlierToday_RollsToTomorrow(t *testing.T) {
	now := time.Date(2026, 6, 7, 4, 30, 0, 0, time.Local)
	got := nextFire(now, 3, 0)
	want := time.Date(2026, 6, 8, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("nextFire(04:30, 03:00) = %v, want %v", got, want)
	}
}

func TestNextFire_ExactlyNow_RollsToTomorrow(t *testing.T) {
	now := time.Date(2026, 6, 7, 3, 0, 0, 0, time.Local)
	got := nextFire(now, 3, 0)
	want := time.Date(2026, 6, 8, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("nextFire(equal-now) = %v, want %v", got, want)
	}
}

func TestPruneOldDumps_DeletesOldKeepsNew(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local)
	old := filepath.Join(dir, "maestro-2026-05-01-0300.dump")
	fresh := filepath.Join(dir, "maestro-2026-06-06-0300.dump")
	other := filepath.Join(dir, "not-a-dump.txt")
	wrongPattern := filepath.Join(dir, "maestro-backup.dump")
	for _, p := range []string{old, fresh, other, wrongPattern} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	// Age `old` to 30 days ago, fresh to 1 day ago, leave the
	// non-matching files untouched (we still expect them to survive).
	if err := os.Chtimes(old, now.Add(-30*24*time.Hour), now.Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(fresh, now.Add(-24*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}
	if err := os.Chtimes(other, now.Add(-365*24*time.Hour), now.Add(-365*24*time.Hour)); err != nil {
		t.Fatalf("chtimes other: %v", err)
	}
	if err := os.Chtimes(wrongPattern, now.Add(-365*24*time.Hour), now.Add(-365*24*time.Hour)); err != nil {
		t.Fatalf("chtimes wrongPattern: %v", err)
	}

	if err := pruneOldDumps(dir, 7, now); err != nil {
		t.Fatalf("pruneOldDumps: %v", err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("expected old dump to be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("expected fresh dump to survive, got err = %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("expected non-dump file to survive, got err = %v", err)
	}
	if _, err := os.Stat(wrongPattern); err != nil {
		t.Errorf("expected non-matching dump-ish file to survive, got err = %v", err)
	}
}

func TestPruneOldDumps_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := pruneOldDumps(dir, 7, time.Now()); err != nil {
		t.Errorf("pruneOldDumps on empty dir: %v", err)
	}
}

func TestSplitPassword(t *testing.T) {
	cases := []struct{ name, in, wantDSN, wantPass string }{
		{"url with password", "postgres://u:s3cr3t@db:5432/maestro?sslmode=disable", "postgres://u@db:5432/maestro?sslmode=disable", "s3cr3t"},
		{"url without password", "postgres://u@db:5432/maestro", "postgres://u@db:5432/maestro", ""},
		{"url without user", "postgres://db:5432/maestro", "postgres://db:5432/maestro", ""},
		{"keyword form is left alone", "host=db user=u password=s3cr3t dbname=maestro", "host=db user=u password=s3cr3t dbname=maestro", ""},
		{"escaped characters survive", "postgres://u:p%40ss%2Fword@db/maestro", "postgres://u@db/maestro", "p@ss/word"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn, pass := splitPassword(tc.in)
			if dsn != tc.wantDSN || pass != tc.wantPass {
				t.Errorf("splitPassword(%q) = (%q, %q), want (%q, %q)", tc.in, dsn, pass, tc.wantDSN, tc.wantPass)
			}
		})
	}
}

// fakePgDump puts a stand-in pg_dump first on PATH. It writes a file
// where it is told, with a deliberately loose mode, and records how it
// was invoked.
func fakePgDump(t *testing.T) (recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "invocation")
	script := "#!/bin/sh\n" +
		"{ echo \"args: $*\"; echo \"pgpassword: ${PGPASSWORD-}\"; } > \"" + recordPath + "\"\n" +
		"for a in \"$@\"; do case \"$a\" in --file=*) out=\"${a#--file=}\";; esac; done\n" +
		"umask 0022\n" +
		"echo dump-contents > \"$out\"\n" +
		"chmod 0644 \"$out\"\n"
	if err := os.WriteFile(filepath.Join(dir, "pg_dump"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return recordPath
}

// The dump is the whole database: it must land 0600, and the password
// must not be visible in the process arguments.
func TestDumpOnce_WritesPrivateFileAndHidesPassword(t *testing.T) {
	record := fakePgDump(t)
	dir := t.TempDir()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.Local)
	err := dumpOnce(t.Context(), Config{
		Dir:         dir,
		DatabaseURL: "postgres://u:s3cr3t@db:5432/maestro",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("dumpOnce: %v", err)
	}

	final := filepath.Join(dir, FilePrefix+"2026-09-20-0300"+FileSuffix)
	info, err := os.Stat(final)
	if err != nil {
		t.Fatalf("dump not written: %v", err)
	}
	if got := info.Mode().Perm(); got != filePerm {
		t.Errorf("dump mode = %#o, want %#o", got, filePerm)
	}
	if _, err := os.Stat(final + ".tmp"); !os.IsNotExist(err) {
		t.Error("the .tmp file survived a successful dump")
	}

	invocation, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("no invocation recorded: %v", err)
	}
	args, _, _ := strings.Cut(string(invocation), "\n")
	if strings.Contains(args, "s3cr3t") {
		t.Errorf("password passed on the command line: %s", args)
	}
	if !strings.Contains(string(invocation), "pgpassword: s3cr3t") {
		t.Errorf("password not passed through the environment: %s", invocation)
	}
}

// Run tightens a directory an earlier version left at 0755.
func TestRun_TightensExistingBackupDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(ctx, Config{
		Dir:      dir,
		At:       "03:00",
		KeepDays: 7,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != dirPerm {
		t.Errorf("backup dir mode = %#o, want %#o", got, dirPerm)
	}
}

// A restart mid-dump leaves a .tmp behind. Retention matches only the
// final name, so these need cleaning up on their own.
func TestPruneOldDumps_RemovesOrphanTempFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 20, 3, 0, 0, 0, time.Local)
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), filePerm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldTmp := write(FilePrefix+"2026-09-01-0300"+FileSuffix+".tmp", 48*time.Hour)
	freshTmp := write(FilePrefix+"2026-09-20-0300"+FileSuffix+".tmp", time.Minute)
	recentDump := write(FilePrefix+"2026-09-19-0300"+FileSuffix, 24*time.Hour)
	unrelated := write("notes.txt.tmp", 72*time.Hour)

	if err := pruneOldDumps(dir, 7, now); err != nil {
		t.Fatalf("pruneOldDumps: %v", err)
	}
	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Error("an orphan .tmp older than a day survived")
	}
	for _, keep := range []string{freshTmp, recentDump, unrelated} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s should have been kept: %v", filepath.Base(keep), err)
		}
	}
}
