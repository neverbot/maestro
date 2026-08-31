// Package config parses and validates the process environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
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

// placeholderSessionKey is the example value shipped in compose.yml. It is
// exactly 32 bytes and would otherwise pass validation, so it is rejected by
// name to stop it from ever running an instance in production.
const placeholderSessionKey = "change-me-change-me-change-me-32ch"

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
	SessionKey          string
	FirstAdminEmail     string
	FirstAdminPassword  string
	AllowedEmailDomains []string
	RegistrationMode    RegistrationMode
	Argon2              Argon2Params
}

// LogValue redacts secrets so a stray slog.Any("config", cfg) never leaks
// SessionKey or FirstAdminPassword; this repository, and its logs, are public.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("addr", c.Addr),
		slog.String("database_url", c.DatabaseURL),
		slog.String("session_key", "REDACTED"),
		slog.String("first_admin_email", c.FirstAdminEmail),
		slog.String("first_admin_password", "REDACTED"),
		slog.Any("allowed_email_domains", c.AllowedEmailDomains),
		slog.String("registration_mode", string(c.RegistrationMode)),
	)
}

// Load reads configuration through the given lookup function, so tests can
// supply an environment without touching the real one.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:               orDefault(getenv("MAESTRO_ADDR"), ":8080"),
		DatabaseURL:        getenv("DATABASE_URL"),
		SessionKey:         getenv("SESSION_KEY"),
		FirstAdminEmail:    strings.ToLower(strings.TrimSpace(getenv("FIRST_ADMIN_EMAIL"))),
		FirstAdminPassword: getenv("FIRST_ADMIN_PASSWORD"),
		RegistrationMode:   RegistrationMode(orDefault(getenv("REGISTRATION_MODE"), string(RegistrationInviteOnly))),
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
	if len(cfg.SessionKey) < 32 {
		return Config{}, errors.New("SESSION_KEY must be at least 32 bytes")
	}
	if cfg.SessionKey == placeholderSessionKey {
		return Config{}, errors.New("SESSION_KEY must not be the placeholder value; generate one with `openssl rand -base64 32`")
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
