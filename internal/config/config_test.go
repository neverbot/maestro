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

// Every setting Load refuses, and the name it must put in the refusal so
// an operator knows which line of their compose file to look at.
func TestLoadRefusesASettingAndNamesIt(t *testing.T) {
	for _, tc := range []struct{ key, value, mentions string }{
		{"TRUSTED_PROXY_COUNT", "not-a-number", "TRUSTED_PROXY_COUNT"},
		{"TRUSTED_PROXY_COUNT", "-1", "TRUSTED_PROXY_COUNT"},
		{"SESSION_TTL", "not-a-duration", "SESSION_TTL"},
		{"SESSION_TTL", "0h", "SESSION_TTL"},
		{"SESSION_TTL", "-1h", "SESSION_TTL"},
		{"INVITE_TTL", "not-a-duration", "INVITE_TTL"},
		{"INVITE_TTL", "0h", "INVITE_TTL"},
		{"INVITE_TTL", "-1h", "INVITE_TTL"},
		{"INVITE_TTL", "4320h", "INVITE_TTL"}, // 180 days, above the 90-day maximum
		{"REGISTRATION_MODE", "open_bar", ""},
		{"REGISTRATION_MODE", "domain_open", "ALLOWED_EMAIL_DOMAINS"},
		{"ALLOWED_EMAIL_DOMAINS", "@example.test", ""},
		{"ALLOWED_EMAIL_DOMAINS", "https://example.test", ""},
		{"ALLOWED_EMAIL_DOMAINS", "stu dio.com", ""},
		{"MAESTRO_ADDR", "8080", "MAESTRO_ADDR"},
		{"FIRST_ADMIN_PASSWORD_RESET", "yes please", "FIRST_ADMIN_PASSWORD_RESET"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://localhost/maestro", tc.key: tc.value}
			_, err := Load(func(k string) string { return env[k] })
			assert.Must(t, err != nil, "%s=%q was accepted", tc.key, tc.value)
			assert.Must(t, tc.mentions == "" || strings.Contains(err.Error(), tc.mentions),
				"error = %q, want it to mention %s", err, tc.mentions)
		})
	}
}

// What Load makes of a setting it accepts, and what it leaves when the
// setting is absent.
func TestLoadReadsASetting(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		want             func(Config) (any, any)
	}{
		{"a proxy count", "TRUSTED_PROXY_COUNT", "1",
			func(c Config) (any, any) { return c.TrustedProxyCount, 1 }},
		{"a session lifetime", "SESSION_TTL", "168h",
			func(c Config) (any, any) { return c.SessionTTL, 168 * time.Hour }},
		{"an invitation lifetime", "INVITE_TTL", "48h",
			func(c Config) (any, any) { return c.InviteTTL, 48 * time.Hour }},
		{"no first-admin reset, which is the default", "", "",
			func(c Config) (any, any) { return c.FirstAdminPasswordReset, false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://localhost/maestro"}
			if tc.key != "" {
				env[tc.key] = tc.value
			}
			cfg, err := Load(func(k string) string { return env[k] })
			assert.NoErr(t, err, "Load")
			got, want := tc.want(cfg)
			assert.Must(t, got == want, "%s = %v, want %v", tc.key, got, want)
		})
	}
}
