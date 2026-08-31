package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	env := map[string]string{
		"SESSION_KEY": "0123456789abcdef0123456789abcdef",
	}
	_, err := Load(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("error = %q, want it to mention DATABASE_URL", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "0123456789abcdef0123456789abcdef",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.RegistrationMode != RegistrationInviteOnly {
		t.Fatalf("RegistrationMode = %q, want invite_only", cfg.RegistrationMode)
	}
	if len(cfg.AllowedEmailDomains) != 0 {
		t.Fatalf("AllowedEmailDomains = %v, want empty", cfg.AllowedEmailDomains)
	}
	if cfg.SessionTTL != 720*time.Hour {
		t.Fatalf("SessionTTL = %v, want 720h", cfg.SessionTTL)
	}
}

func TestLoadParsesSessionTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "0123456789abcdef0123456789abcdef",
		"SESSION_TTL":  "168h",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SessionTTL != 168*time.Hour {
		t.Fatalf("SessionTTL = %v, want 168h", cfg.SessionTTL)
	}
}

func TestLoadRejectsInvalidSessionTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "0123456789abcdef0123456789abcdef",
		"SESSION_TTL":  "not-a-duration",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "SESSION_TTL") {
		t.Fatalf("error = %q, want it to mention SESSION_TTL", err)
	}
}

func TestLoadRejectsNonPositiveSessionTTL(t *testing.T) {
	for _, raw := range []string{"0h", "-1h"} {
		env := map[string]string{
			"DATABASE_URL": "postgres://localhost/maestro",
			"SESSION_KEY":  "0123456789abcdef0123456789abcdef",
			"SESSION_TTL":  raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		err = mustErr(t, cfg, err)
		if !strings.Contains(err.Error(), "SESSION_TTL") {
			t.Fatalf("SESSION_TTL=%q: error = %q, want it to mention SESSION_TTL", raw, err)
		}
	}
}

func TestLoadParsesDomainsAndMode(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":          "postgres://localhost/maestro",
		"SESSION_KEY":           "0123456789abcdef0123456789abcdef",
		"ALLOWED_EMAIL_DOMAINS": "Studio.com, example.org ",
		"REGISTRATION_MODE":     "domain_open",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"studio.com", "example.org"}
	if len(cfg.AllowedEmailDomains) != len(want) {
		t.Fatalf("domains = %v, want %v", cfg.AllowedEmailDomains, want)
	}
	for i := range want {
		if cfg.AllowedEmailDomains[i] != want[i] {
			t.Fatalf("domains[%d] = %q, want %q", i, cfg.AllowedEmailDomains[i], want[i])
		}
	}
	if cfg.RegistrationMode != RegistrationDomainOpen {
		t.Fatalf("RegistrationMode = %q, want domain_open", cfg.RegistrationMode)
	}
}

func TestLoadRejectsUnknownRegistrationMode(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":      "postgres://localhost/maestro",
		"SESSION_KEY":       "0123456789abcdef0123456789abcdef",
		"REGISTRATION_MODE": "open_bar",
	}
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected an error for an unknown registration mode")
	}
}

func TestLoadRejectsDomainOpenWithoutDomains(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":      "postgres://localhost/maestro",
		"SESSION_KEY":       "0123456789abcdef0123456789abcdef",
		"REGISTRATION_MODE": "domain_open",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "ALLOWED_EMAIL_DOMAINS") {
		t.Fatalf("error = %q, want it to mention ALLOWED_EMAIL_DOMAINS", err)
	}
}

func TestLoadRejectsInvalidDomainEntry(t *testing.T) {
	cases := []string{"@studio.com", "https://studio.com", "stu dio.com"}
	for _, entry := range cases {
		env := map[string]string{
			"DATABASE_URL":          "postgres://localhost/maestro",
			"SESSION_KEY":           "0123456789abcdef0123456789abcdef",
			"ALLOWED_EMAIL_DOMAINS": entry,
		}
		if _, err := Load(func(k string) string { return env[k] }); err == nil {
			t.Errorf("entry %q: expected an error, got none", entry)
		}
	}
}

func TestLoadRejectsShortSessionKey(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "tooshort10",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "SESSION_KEY") {
		t.Fatalf("error = %q, want it to mention SESSION_KEY", err)
	}
}

func TestLoadRejectsPlaceholderSessionKey(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "change-me-change-me-change-me-32ch",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "SESSION_KEY") {
		t.Fatalf("error = %q, want it to mention SESSION_KEY", err)
	}
}

func TestLoadRejectsInvalidAddr(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_KEY":  "0123456789abcdef0123456789abcdef",
		"MAESTRO_ADDR": "8080",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "MAESTRO_ADDR") {
		t.Fatalf("error = %q, want it to mention MAESTRO_ADDR", err)
	}
}

func TestEmailAllowed(t *testing.T) {
	unrestricted := Config{}
	if !unrestricted.EmailAllowed("anyone@anywhere.net") {
		t.Error("empty domain list must allow everything")
	}
	if unrestricted.EmailAllowed("not-an-email") {
		t.Error("an address without @ must never be allowed, even unrestricted")
	}

	restricted := Config{AllowedEmailDomains: []string{"studio.com"}}
	cases := map[string]bool{
		"designer@studio.com":  true,
		"designer@STUDIO.com":  true,
		"designer@other.com":   false,
		"not-an-email":         false,
		"designer@studio.com ": true,
	}
	for email, want := range cases {
		if got := restricted.EmailAllowed(email); got != want {
			t.Errorf("EmailAllowed(%q) = %v, want %v", email, got, want)
		}
	}

	mixedCase := Config{AllowedEmailDomains: []string{"Studio.com"}}
	if !mixedCase.EmailAllowed("designer@studio.com") {
		t.Error("EmailAllowed must compare case-insensitively even against a mixed-case literal")
	}
}

func mustErr(t *testing.T, cfg Config, err error) error {
	t.Helper()
	if err == nil {
		t.Fatalf("Load: expected an error, got cfg = %+v", cfg)
	}
	return err
}
