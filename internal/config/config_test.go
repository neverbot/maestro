package config

import "testing"

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error when DATABASE_URL is unset")
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

func TestEmailAllowed(t *testing.T) {
	unrestricted := Config{}
	if !unrestricted.EmailAllowed("anyone@anywhere.net") {
		t.Fatal("empty domain list must allow everything")
	}

	restricted := Config{AllowedEmailDomains: []string{"studio.com"}}
	cases := map[string]bool{
		"designer@studio.com": true,
		"designer@STUDIO.com": true,
		"designer@other.com":  false,
		"not-an-email":        false,
	}
	for email, want := range cases {
		if got := restricted.EmailAllowed(email); got != want {
			t.Fatalf("EmailAllowed(%q) = %v, want %v", email, got, want)
		}
	}
}
