package config

import (
	"strings"
	"testing"
	"time"

	"github.com/neverbot/maestro/internal/assert"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	env := map[string]string{}
	_, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err != nil, "expected an error when DATABASE_URL is unset")
	assert.Must(t, strings.Contains(err.Error(), "DATABASE_URL"), "error = %q, want it to mention DATABASE_URL", err)
}

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	assert.Must(t, cfg.Addr == ":8080", "Addr = %q, want :8080", cfg.Addr)
	assert.Must(t, cfg.RegistrationMode == RegistrationInviteOnly, "RegistrationMode = %q, want invite_only", cfg.RegistrationMode)
	assert.Must(t, len(cfg.AllowedEmailDomains) == 0, "AllowedEmailDomains = %v, want empty", cfg.AllowedEmailDomains)
	assert.Must(t, cfg.SessionTTL == 720*time.Hour, "SessionTTL = %v, want 720h", cfg.SessionTTL)
	assert.Must(t, cfg.InviteTTL == 14*24*time.Hour, "InviteTTL = %v, want 336h", cfg.InviteTTL)
	assert.Must(t, cfg.TrustedProxyCount == 0, "TrustedProxyCount = %d, want 0 (a directly exposed instance by default)", cfg.TrustedProxyCount)
}

func TestLoadParsesTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "1",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	assert.Must(t, cfg.TrustedProxyCount == 1, "TrustedProxyCount = %d, want 1", cfg.TrustedProxyCount)
}

func TestLoadRejectsInvalidTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "not-a-number",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "TRUSTED_PROXY_COUNT"), "error = %q, want it to mention TRUSTED_PROXY_COUNT", err)
}

func TestLoadRejectsNegativeTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "-1",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "TRUSTED_PROXY_COUNT"), "error = %q, want it to mention TRUSTED_PROXY_COUNT", err)
}

func TestLoadParsesSessionTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_TTL":  "168h",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	assert.Must(t, cfg.SessionTTL == 168*time.Hour, "SessionTTL = %v, want 168h", cfg.SessionTTL)
}

func TestLoadRejectsInvalidSessionTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"SESSION_TTL":  "not-a-duration",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "SESSION_TTL"), "error = %q, want it to mention SESSION_TTL", err)
}

func TestLoadRejectsNonPositiveSessionTTL(t *testing.T) {
	for _, raw := range []string{"0h", "-1h"} {
		env := map[string]string{
			"DATABASE_URL": "postgres://localhost/maestro",
			"SESSION_TTL":  raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		err = mustErr(t, cfg, err)
		assert.Must(t, strings.Contains(err.Error(), "SESSION_TTL"), "SESSION_TTL=%q: error = %q, want it to mention SESSION_TTL", raw, err)
	}
}

func TestLoadParsesInviteTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "48h",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	assert.Must(t, cfg.InviteTTL == 48*time.Hour, "InviteTTL = %v, want 48h", cfg.InviteTTL)
}

func TestLoadRejectsInvalidInviteTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "not-a-duration",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "INVITE_TTL"), "error = %q, want it to mention INVITE_TTL", err)
}

func TestLoadRejectsNonPositiveInviteTTL(t *testing.T) {
	for _, raw := range []string{"0h", "-1h"} {
		env := map[string]string{
			"DATABASE_URL": "postgres://localhost/maestro",
			"INVITE_TTL":   raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		err = mustErr(t, cfg, err)
		assert.Must(t, strings.Contains(err.Error(), "INVITE_TTL"), "INVITE_TTL=%q: error = %q, want it to mention INVITE_TTL", raw, err)
	}
}

func TestLoadRejectsInviteTTLAboveMax(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "4320h", // 180 days, above the 90-day maximum
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "INVITE_TTL"), "error = %q, want it to mention INVITE_TTL", err)
}

func TestLoadParsesDomainsAndMode(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":          "postgres://localhost/maestro",
		"ALLOWED_EMAIL_DOMAINS": "Example.test, partner.test ",
		"REGISTRATION_MODE":     "domain_open",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	want := []string{"example.test", "partner.test"}
	assert.Must(t, len(cfg.AllowedEmailDomains) == len(want), "domains = %v, want %v", cfg.AllowedEmailDomains, want)
	for i := range want {
		if cfg.AllowedEmailDomains[i] != want[i] {
			t.Fatalf("domains[%d] = %q, want %q", i, cfg.AllowedEmailDomains[i], want[i])
		}
	}
	assert.Must(t, cfg.RegistrationMode == RegistrationDomainOpen, "RegistrationMode = %q, want domain_open", cfg.RegistrationMode)
}

func TestLoadRejectsUnknownRegistrationMode(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":      "postgres://localhost/maestro",
		"REGISTRATION_MODE": "open_bar",
	}
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("expected an error for an unknown registration mode")
	}
}

func TestLoadRejectsDomainOpenWithoutDomains(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":      "postgres://localhost/maestro",
		"REGISTRATION_MODE": "domain_open",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "ALLOWED_EMAIL_DOMAINS"), "error = %q, want it to mention ALLOWED_EMAIL_DOMAINS", err)
}

func TestLoadRejectsInvalidDomainEntry(t *testing.T) {
	cases := []string{"@example.test", "https://example.test", "stu dio.com"}
	for _, entry := range cases {
		env := map[string]string{
			"DATABASE_URL":          "postgres://localhost/maestro",
			"ALLOWED_EMAIL_DOMAINS": entry,
		}
		if _, err := Load(func(k string) string { return env[k] }); err == nil {
			t.Errorf("entry %q: expected an error, got none", entry)
		}
	}
}

func TestLoadRejectsInvalidAddr(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"MAESTRO_ADDR": "8080",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	assert.Must(t, strings.Contains(err.Error(), "MAESTRO_ADDR"), "error = %q, want it to mention MAESTRO_ADDR", err)
}

func TestEmailAllowed(t *testing.T) {
	unrestricted := Config{}
	assert.Should(t, unrestricted.EmailAllowed("anyone@anywhere.test"), "empty domain list must allow everything")
	assert.Should(t, !unrestricted.EmailAllowed("not-an-email"), "an address without @ must never be allowed, even unrestricted")

	restricted := Config{AllowedEmailDomains: []string{"example.test"}}
	cases := map[string]bool{
		"designer@example.test":  true,
		"designer@EXAMPLE.test":  true,
		"designer@other.test":    false,
		"not-an-email":           false,
		"designer@example.test ": true,
	}
	for email, want := range cases {
		if got := restricted.EmailAllowed(email); got != want {
			t.Errorf("EmailAllowed(%q) = %v, want %v", email, got, want)
		}
	}

	mixedCase := Config{AllowedEmailDomains: []string{"Example.test"}}
	assert.Should(t, mixedCase.EmailAllowed("designer@example.test"), "EmailAllowed must compare case-insensitively even against a mixed-case literal")
}

func mustErr(t *testing.T, cfg Config, err error) error {
	t.Helper()
	assert.Must(t, err != nil, "Load: expected an error, got cfg = %+v", cfg)
	return err
}

func TestLoadDefaultsFirstAdminPasswordResetToFalse(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err == nil, "Load: %v", err)
	assert.Must(t, !cfg.FirstAdminPasswordReset, "FirstAdminPasswordReset = true, want false when FIRST_ADMIN_PASSWORD_RESET is unset")
}

func TestLoadParsesFirstAdminPasswordReset(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "1", "t", "True"} {
		env := map[string]string{
			"DATABASE_URL":               "postgres://localhost/maestro",
			"FIRST_ADMIN_PASSWORD_RESET": raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		assert.Must(t, err == nil, "Load(%q): %v", raw, err)
		assert.Must(t, cfg.FirstAdminPasswordReset, "FIRST_ADMIN_PASSWORD_RESET=%q gave FirstAdminPasswordReset = false, want true", raw)
	}
	for _, raw := range []string{"false", "FALSE", "0", "f"} {
		env := map[string]string{
			"DATABASE_URL":               "postgres://localhost/maestro",
			"FIRST_ADMIN_PASSWORD_RESET": raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		assert.Must(t, err == nil, "Load(%q): %v", raw, err)
		assert.Must(t, !cfg.FirstAdminPasswordReset, "FIRST_ADMIN_PASSWORD_RESET=%q gave FirstAdminPasswordReset = true, want false", raw)
	}
}

func TestLoadRejectsInvalidFirstAdminPasswordReset(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":               "postgres://localhost/maestro",
		"FIRST_ADMIN_PASSWORD_RESET": "yes please",
	}
	_, err := Load(func(k string) string { return env[k] })
	assert.Must(t, err != nil, "expected an error for an unparseable FIRST_ADMIN_PASSWORD_RESET")
	assert.Must(t, strings.Contains(err.Error(), "FIRST_ADMIN_PASSWORD_RESET"), "error = %q, want it to mention FIRST_ADMIN_PASSWORD_RESET", err)
}
