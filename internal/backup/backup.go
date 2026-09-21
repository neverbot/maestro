// Package backup runs periodic pg_dump backups in-process.
//
// **The readme said this was deliberately missing**, under the heading
// that names what a self-hoster has to do themselves, and for a while
// that was an honest trade: backing up an instance means backing up its
// Postgres volume like any other database. It stopped being honest the
// moment the product started holding a game's whole design — four
// hundred missions nobody has a second copy of — behind a single
// container somebody runs on a machine in their house.
//
// Disabled when Config.Dir is empty, which is the default: an instance
// that says nothing about backups gets exactly what it got before. When
// enabled, one goroutine sleeps until the next MAESTRO_BACKUP_AT slot in
// local time, shells out to `pg_dump --format=custom` against
// Config.DatabaseURL, writes the dump atomically, and prunes files older
// than KeepDays. Errors are logged and the loop continues; the goroutine
// never returns while ctx is alive, because a backup that stops after
// one bad night is a backup nobody notices is gone.
//
// **It is Nottario's, ported deliberately and not cloned by habit.**
// That implementation has been through a security pass this one starts
// from rather than repeats: the dump is 0600 in a 0700 directory, an
// existing directory is tightened on start, the password reaches
// pg_dump through the environment so it is not in `ps`, and orphan
// `.tmp` files from an interrupted dump are swept. The one thing that
// could not come with it is the image: Maestro's runtime was distroless
// and distroless has no pg_dump.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config holds the inputs for Run. Dir empty disables backups.
type Config struct {
	Dir         string
	DatabaseURL string
	At          string // "HH:MM"
	KeepDays    int
	Logger      *slog.Logger
	// Now allows tests to inject time.
	Now func() time.Time
}

// FilePrefix and FileSuffix bracket every dump filename.
const (
	FilePrefix = "maestro-"
	FileSuffix = ".dump"
)

// A dump is the entire database in one file: every game's content, the
// prose, the user table with its email addresses, and the api_tokens
// rows. It is readable by its owner and nobody else, and so is the
// directory holding it — a backup directory on a shared host must not
// hand the instance to every local account.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// tmpAge is how long an orphan .tmp may sit before being cleaned up.
// A dump interrupted by a restart leaves one behind, and the final name
// is what retention matches, so without this they accumulate for ever.
// Well clear of any real dump's runtime.
const tmpAge = 24 * time.Hour

// Run blocks until ctx is cancelled. Returns nil immediately if
// Config.Dir is empty. Safe to call once from main().
func Run(ctx context.Context, c Config) error {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Dir == "" {
		c.Logger.Info("backups disabled (MAESTRO_BACKUP_DIR not set)")
		return nil
	}
	if c.At == "" {
		c.At = "03:00"
	}
	if c.KeepDays <= 0 {
		c.KeepDays = 7
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	h, m, err := parseClock(c.At)
	if err != nil {
		return fmt.Errorf("parse MAESTRO_BACKUP_AT: %w", err)
	}
	if err := os.MkdirAll(c.Dir, dirPerm); err != nil {
		return fmt.Errorf("mkdir backup dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone, so tighten it
	// explicitly: a directory created by an earlier version, or by the
	// operator, is the common case. A mount we do not own refuses the
	// chmod; that is the operator's call to make, so carry on.
	if err := os.Chmod(c.Dir, dirPerm); err != nil {
		c.Logger.Warn("could not tighten backup dir permissions", "dir", c.Dir, "err", err)
	}
	c.Logger.Info("backups enabled", "dir", c.Dir, "at", c.At, "keep_days", c.KeepDays)
	for {
		next := nextFire(c.Now(), h, m)
		wait := time.Until(next)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if err := dumpOnce(ctx, c); err != nil {
			c.Logger.Error("backup failed", "err", err)
			continue
		}
		if err := pruneOldDumps(c.Dir, c.KeepDays, c.Now()); err != nil {
			c.Logger.Warn("backup prune failed", "err", err)
		}
	}
}

func parseClock(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("expected HH:MM")
	}
	h, herr := strconv.Atoi(parts[0])
	m, merr := strconv.Atoi(parts[1])
	if herr != nil || merr != nil {
		return 0, 0, errors.New("non-numeric HH or MM")
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, errors.New("HH must be 0-23, MM must be 0-59")
	}
	return h, m, nil
}

func nextFire(now time.Time, h, m int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !t.After(now) {
		t = t.Add(24 * time.Hour)
	}
	return t
}

func dumpOnce(ctx context.Context, c Config) error {
	now := c.Now()
	name := fmt.Sprintf("%s%s%s", FilePrefix, now.Format("2006-01-02-1504"), FileSuffix)
	tmp := filepath.Join(c.Dir, name+".tmp")
	final := filepath.Join(c.Dir, name)
	dsn, password := splitPassword(c.DatabaseURL)
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--file="+tmp, dsn)
	// The password goes through the environment: process arguments are
	// world-readable in `ps` for as long as the dump runs.
	if password != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("pg_dump: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// pg_dump creates the file under its own umask, so tighten it before
	// it takes its final name. The brief window while it is still a .tmp
	// is covered by the directory being 0700.
	if err := os.Chmod(tmp, filePerm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod dump: %w", err)
	}
	// **Atomic, so a reader never meets half a dump.** A backup agent
	// copying this directory on a schedule of its own would otherwise
	// pick up a file pg_dump is still writing, and a truncated custom
	// dump restores nothing.
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	c.Logger.Info("backup written", "file", final)
	return nil
}

var dumpNameRe = regexp.MustCompile(`^` + FilePrefix + `\d{4}-\d{2}-\d{2}-\d{4}` + regexp.QuoteMeta(FileSuffix) + `$`)

var tmpNameRe = regexp.MustCompile(`^` + FilePrefix + `\d{4}-\d{2}-\d{2}-\d{4}` + regexp.QuoteMeta(FileSuffix+".tmp") + `$`)

// splitPassword takes the password out of a postgres:// URL, returning
// the URL without it plus the password itself, for the caller to pass
// through the environment. A DSN in keyword form (host=… password=…) or
// one with no password is returned untouched.
func splitPassword(dsn string) (string, string) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn, ""
	}
	password, ok := u.User.Password()
	if !ok {
		return dsn, ""
	}
	u.User = url.User(u.User.Username())
	return u.String(), password
}

// pruneOldDumps is the retention, and it is **matched by name and not by
// "everything in the directory"**: an operator who keeps a hand-made
// dump, a note or an unrelated archive beside these keeps it. Only files
// this package wrote are ever removed.
func pruneOldDumps(dir string, keepDays int, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	tmpCutoff := now.Add(-tmpAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var deadline time.Time
		switch {
		case dumpNameRe.MatchString(e.Name()):
			deadline = cutoff
		case tmpNameRe.MatchString(e.Name()):
			deadline = tmpCutoff
		default:
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().Before(deadline) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}
