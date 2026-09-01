package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	env := map[string]string{}
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
	if cfg.InviteTTL != 14*24*time.Hour {
		t.Fatalf("InviteTTL = %v, want 336h", cfg.InviteTTL)
	}
	if cfg.TrustedProxyCount != 0 {
		t.Fatalf("TrustedProxyCount = %d, want 0 (a directly exposed instance by default)", cfg.TrustedProxyCount)
	}
}

func TestLoadParsesTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "1",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TrustedProxyCount != 1 {
		t.Fatalf("TrustedProxyCount = %d, want 1", cfg.TrustedProxyCount)
	}
}

func TestLoadRejectsInvalidTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "not-a-number",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "TRUSTED_PROXY_COUNT") {
		t.Fatalf("error = %q, want it to mention TRUSTED_PROXY_COUNT", err)
	}
}

func TestLoadRejectsNegativeTrustedProxyCount(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/maestro",
		"TRUSTED_PROXY_COUNT": "-1",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "TRUSTED_PROXY_COUNT") {
		t.Fatalf("error = %q, want it to mention TRUSTED_PROXY_COUNT", err)
	}
}

func TestLoadParsesSessionTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
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
			"SESSION_TTL":  raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		err = mustErr(t, cfg, err)
		if !strings.Contains(err.Error(), "SESSION_TTL") {
			t.Fatalf("SESSION_TTL=%q: error = %q, want it to mention SESSION_TTL", raw, err)
		}
	}
}

func TestLoadParsesInviteTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "48h",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InviteTTL != 48*time.Hour {
		t.Fatalf("InviteTTL = %v, want 48h", cfg.InviteTTL)
	}
}

func TestLoadRejectsInvalidInviteTTL(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "not-a-duration",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "INVITE_TTL") {
		t.Fatalf("error = %q, want it to mention INVITE_TTL", err)
	}
}

func TestLoadRejectsNonPositiveInviteTTL(t *testing.T) {
	for _, raw := range []string{"0h", "-1h"} {
		env := map[string]string{
			"DATABASE_URL": "postgres://localhost/maestro",
			"INVITE_TTL":   raw,
		}
		cfg, err := Load(func(k string) string { return env[k] })
		err = mustErr(t, cfg, err)
		if !strings.Contains(err.Error(), "INVITE_TTL") {
			t.Fatalf("INVITE_TTL=%q: error = %q, want it to mention INVITE_TTL", raw, err)
		}
	}
}

func TestLoadRejectsInviteTTLAboveMax(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/maestro",
		"INVITE_TTL":   "4320h", // 180 days, above the 90-day maximum
	}
	cfg, err := Load(func(k string) string { return env[k] })
	err = mustErr(t, cfg, err)
	if !strings.Contains(err.Error(), "INVITE_TTL") {
		t.Fatalf("error = %q, want it to mention INVITE_TTL", err)
	}
}

func TestLoadParsesDomainsAndMode(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":          "postgres://localhost/maestro",
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
	if !strings.Contains(err.Error(), "ALLOWED_EMAIL_DOMAINS") {
		t.Fatalf("error = %q, want it to mention ALLOWED_EMAIL_DOMAINS", err)
	}
}

func TestLoadRejectsInvalidDomainEntry(t *testing.T) {
	cases := []string{"@studio.com", "https://studio.com", "stu dio.com"}
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
