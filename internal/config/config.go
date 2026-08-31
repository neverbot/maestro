// Package config parses and validates the process environment.
package config

import (
	"fmt"
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

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if len(cfg.SessionKey) < 32 {
		return Config{}, fmt.Errorf("SESSION_KEY must be at least 32 characters")
	}
	switch cfg.RegistrationMode {
	case RegistrationInviteOnly, RegistrationDomainOpen:
	default:
		return Config{}, fmt.Errorf("REGISTRATION_MODE %q is not one of invite_only, domain_open", cfg.RegistrationMode)
	}

	for _, raw := range strings.Split(getenv("ALLOWED_EMAIL_DOMAINS"), ",") {
		d := strings.ToLower(strings.TrimSpace(raw))
		if d != "" {
			cfg.AllowedEmailDomains = append(cfg.AllowedEmailDomains, d)
		}
	}

	return cfg, nil
}

// EmailAllowed reports whether an address may register on this instance.
func (c Config) EmailAllowed(email string) bool {
	if len(c.AllowedEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range c.AllowedEmailDomains {
		if d == domain {
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
