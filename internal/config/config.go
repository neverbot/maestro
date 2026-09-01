// Package config parses and validates the process environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// RegistrationMode decides who may create an account.
type RegistrationMode string

const (
	// RegistrationInviteOnly admits users holding an invite link.
	RegistrationInviteOnly RegistrationMode = "invite_only"
	// RegistrationDomainOpen admits any email in AllowedEmailDomains.
	RegistrationDomainOpen RegistrationMode = "domain_open"
)

// validRegistrationModes lists every accepted RegistrationMode.
var validRegistrationModes = []RegistrationMode{RegistrationInviteOnly, RegistrationDomainOpen}

// defaultSessionTTL is how long a login lasts when SESSION_TTL is unset.
const defaultSessionTTL = 720 * time.Hour

// defaultInviteTTL is how long an invite link stays usable when INVITE_TTL
// is unset. It was a hardcoded constant in internal/identity/invites.go
// (identity.InviteTTL); it now lives here so a studio wanting a
// sprint-length link and an operator wanting a 48-hour one can both set it
// without forking the binary, the same way SESSION_TTL already works.
const defaultInviteTTL = 14 * 24 * time.Hour

// MaxInviteTTL bounds INVITE_TTL and InviteRequest.ExpiresIn (see
// identity.InviteRequest): an operator-set or per-invite lifetime is still
// a bearer credential the moment it exists, and an unbounded one turns a
// single mistaken value, or one caller of InviteRequest, into a
// standing credential of arbitrary duration.
const MaxInviteTTL = 90 * 24 * time.Hour

// Argon2Params are the password hashing cost parameters.
type Argon2Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// Config is the fully resolved runtime configuration.
type Config struct {
	Addr                string
	DatabaseURL         string
	SessionTTL          time.Duration
	InviteTTL           time.Duration
	FirstAdminEmail     string
	FirstAdminPassword  string
	AllowedEmailDomains []string
	RegistrationMode    RegistrationMode
	Argon2              Argon2Params

	// TrustedProxyCount is the number of reverse proxies this instance
	// trusts to sit directly in front of it and to correctly append (never
	// pass through unchanged, never let a client's own value survive) an
	// entry to X-Forwarded-For, and to set X-Forwarded-Proto, on every
	// request. It defaults to zero: a directly exposed instance, where
	// RemoteAddr is the real client and neither header is consulted at
	// all — the same posture web.clientIP and web.setSessionCookie held
	// before this field existed.
	//
	// Getting this wrong is a real failure mode in both directions, not
	// just an inconvenience. Too low (in particular, left at zero behind
	// an actual proxy) does not merely fail to identify the client: every
	// request's RemoteAddr is then the proxy's own address, so a per-IP
	// rate limiter keyed on it collapses into one global bucket shared by
	// every caller on the instance — ten requests from anyone exhausts it
	// for everyone until the window rolls, which is worse than having no
	// limiter at all. Too high, or nonzero on an instance with no proxy in
	// front of it, lets a direct caller forge whichever X-Forwarded-For
	// entry this instance ends up trusting as "the client", defeating the
	// per-IP budget from the other direction. Set it to the actual number
	// of hops between the public internet and this process (usually 1)
	// only once a reverse proxy is confirmed to be there and to behave
	// this way; the default of zero is the only safe value for an
	// instance reachable directly.
	TrustedProxyCount int
}

// LogValue redacts secrets so a stray slog.Any("config", cfg) never leaks
// FirstAdminPassword; this repository, and its logs, are public.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("database_url", c.DatabaseURL),
		slog.String("first_admin_email", c.FirstAdminEmail),
		slog.String("first_admin_password", "REDACTED"),
		slog.Any("allowed_email_domains", c.AllowedEmailDomains),
		slog.String("registration_mode", string(c.RegistrationMode)),
		slog.Duration("session_ttl", c.SessionTTL),
		slog.Duration("invite_ttl", c.InviteTTL),
		slog.Int("trusted_proxy_count", c.TrustedProxyCount),
	)
}

// Load reads configuration through the given lookup function, so tests can
// supply an environment without touching the real one.
//
// SESSION_KEY, once required and validated here (a minimum length, and a
// named rejection of the placeholder value compose.yml shipped), was
// removed by a Task 22 review: nothing outside this package ever read
// Config.SessionKey. Sessions in this product are random tokens stored
// in a table (internal/identity/sessions.go, IssueSession/UserForSession)
// — nothing signs or encrypts anything with a key, so there was never a
// mechanism SESSION_KEY could have been wired into. The readme told
// operators to generate a real value with `openssl rand -base64 32`
// before deploying anywhere but a laptop, in language that implied
// rotating it invalidated sessions; it did nothing, ever. A control that
// exists only as its own validation — required, length-checked, and
// rejecting its own placeholder by name, with zero downstream effect —
// is worse than no control at all: it is validation theater that reads
// as a real security boundary to an operator who has no reason to go
// looking for the one that isn't there, and it cost every deployment a
// secret to generate, store and rotate for a value nothing consulted.
// Deleting it is strictly safer than leaving it inert.
func Load(getenv func(string) string) (Config, error) {
	sessionTTL, err := parsePositiveDuration("SESSION_TTL", getenv("SESSION_TTL"), defaultSessionTTL, 0)
	if err != nil {
		return Config{}, err
	}
	inviteTTL, err := parsePositiveDuration("INVITE_TTL", getenv("INVITE_TTL"), defaultInviteTTL, MaxInviteTTL)
	if err != nil {
		return Config{}, err
	}
	trustedProxyCount, err := parseNonNegativeInt("TRUSTED_PROXY_COUNT", getenv("TRUSTED_PROXY_COUNT"), 0)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Addr:               orDefault(getenv("MAESTRO_ADDR"), ":8080"),
		DatabaseURL:        getenv("DATABASE_URL"),
		SessionTTL:         sessionTTL,
		InviteTTL:          inviteTTL,
		FirstAdminEmail:    strings.ToLower(strings.TrimSpace(getenv("FIRST_ADMIN_EMAIL"))),
		FirstAdminPassword: getenv("FIRST_ADMIN_PASSWORD"),
		RegistrationMode:   RegistrationMode(orDefault(getenv("REGISTRATION_MODE"), string(RegistrationInviteOnly))),
		TrustedProxyCount:  trustedProxyCount,
		Argon2: Argon2Params{
			Time:    3,
			Memory:  64 * 1024,
			Threads: 2,
			KeyLen:  32,
			SaltLen: 16,
		},
	}
	for _, raw := range strings.Split(getenv("ALLOWED_EMAIL_DOMAINS"), ",") {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d != "" {
			cfg.AllowedEmailDomains = append(cfg.AllowedEmailDomains, d)
		}
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	switch cfg.RegistrationMode {
	case RegistrationInviteOnly, RegistrationDomainOpen:
	default:
		return Config{}, fmt.Errorf("REGISTRATION_MODE %q is not one of %s", cfg.RegistrationMode, joinModes(validRegistrationModes))
	}
	if cfg.RegistrationMode == RegistrationDomainOpen && len(cfg.AllowedEmailDomains) == 0 {
		return Config{}, errors.New("REGISTRATION_MODE=domain_open requires ALLOWED_EMAIL_DOMAINS to list at least one domain")
	}
	for _, d := range cfg.AllowedEmailDomains {
		if strings.ContainsAny(d, "@/ \t") {
			return Config{}, fmt.Errorf("ALLOWED_EMAIL_DOMAINS entry %q must be a bare domain, not an email address or URL", d)
		}
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return Config{}, fmt.Errorf("MAESTRO_ADDR %q is not a valid host:port: %w", cfg.Addr, err)
	}

	return cfg, nil
}

// EmailAllowed reports whether an address may register on this instance.
// Matching is exact against AllowedEmailDomains: an address such as
// user@mail.studio.com does not match an allow-listed "studio.com".
func (c Config) EmailAllowed(email string) bool {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	if len(c.AllowedEmailDomains) == 0 {
		return true
	}
	domain := email[at+1:]
	for _, d := range c.AllowedEmailDomains {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

// parsePositiveDuration parses a duration-valued environment variable
// named name, defaulting to def when raw is empty. A duration of zero or
// less is rejected here rather than left for the caller to discover: for
// SESSION_TTL a non-positive value would silently mint already-expired
// sessions (GetSessionUser requires expires_at > now()), turning login
// into a no-op with no error anywhere near the cause, and the same is true
// of INVITE_TTL against GetLiveInvite's own expires_at > now() filter. A
// zero max means unbounded (SESSION_TTL has no ceiling today); a positive
// max rejects anything above it, so a fat-fingered "INVITE_TTL=14y" fails
// at start-up instead of minting a standing credential nobody meant to
// hand out.
func parsePositiveDuration(name, raw string, def, maxD time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a valid duration: %w", name, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", name, raw)
	}
	if maxD > 0 && d > maxD {
		return 0, fmt.Errorf("%s %q exceeds the maximum of %s", name, raw, maxD)
	}
	return d, nil
}

// parseNonNegativeInt parses an integer-valued environment variable named
// name, defaulting to def when raw is empty and rejecting a negative
// value: TRUSTED_PROXY_COUNT is a hop count, and a negative one has no
// meaning worth silently coercing to zero and hiding a typo in an
// operator's environment.
func parseNonNegativeInt(name, raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a valid integer: %w", name, raw, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %q", name, raw)
	}
	return n, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func joinModes(modes []RegistrationMode) string {
	s := make([]string, len(modes))
	for i, m := range modes {
		s[i] = string(m)
	}
	return strings.Join(s, ", ")
}
