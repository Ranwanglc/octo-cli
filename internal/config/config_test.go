package config

import (
	"testing"
)

func TestValidate_MissingToken(t *testing.T) {
	c := &Config{APIBaseURL: "http://localhost", BotToken: ""}
	if err := c.Validate(); err == nil {
		t.Error("expected error for missing OCTO_BOT_TOKEN")
	}
}

func TestValidate_OK(t *testing.T) {
	c := &Config{APIBaseURL: "http://localhost", BotToken: "app_xxx"}
	if err := c.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv(EnvBotToken, "")
	t.Setenv(EnvAPIBaseURL, "")
	t.Setenv(EnvFormat, "")
	t.Setenv(EnvSpaceID, "")

	cfg := Load()
	if cfg.APIBaseURL != "http://127.0.0.1:8080" {
		t.Errorf("APIBaseURL = %q, want default", cfg.APIBaseURL)
	}
	if cfg.Format != "json" {
		t.Errorf("Format = %q, want json", cfg.Format)
	}
}

func TestLoad_ReadsAllEnvVars(t *testing.T) {
	t.Setenv(EnvAPIBaseURL, "http://api.example")
	t.Setenv(EnvBotToken, "app_xxx")
	t.Setenv(EnvSpaceID, "space-1")
	t.Setenv(EnvFormat, "table")

	cfg := Load()
	if cfg.APIBaseURL != "http://api.example" {
		t.Errorf("APIBaseURL = %q", cfg.APIBaseURL)
	}
	if cfg.BotToken != "app_xxx" {
		t.Errorf("BotToken = %q", cfg.BotToken)
	}
	if cfg.SpaceID != "space-1" {
		t.Errorf("SpaceID = %q", cfg.SpaceID)
	}
	if cfg.Format != "table" {
		t.Errorf("Format = %q", cfg.Format)
	}
}

func TestServiceURL_Unified(t *testing.T) {
	cfg := &Config{APIBaseURL: "http://api.example"}
	// All services should return the same API base URL.
	for _, svc := range []string{"matters", "dmworkim", "unknown", ""} {
		if got := cfg.ServiceURL(svc); got != "http://api.example" {
			t.Errorf("ServiceURL(%q) = %q, want API base URL", svc, got)
		}
	}
}

// TestServiceURL_SpecEnvName pins the fail-loud path added for spec-driven
// backends other than the bot API: when the service key is a well-formed
// env-var name (all-caps + underscore), ServiceURL returns os.Getenv(name)
// verbatim — empty means "no base URL configured", so client.Do reports the
// mistake with a hint at the correct env var. Falling back to APIBaseURL here
// would silently route html-domain traffic to the bot backend.
func TestServiceURL_SpecEnvName(t *testing.T) {
	cfg := &Config{APIBaseURL: "http://bot.example"}

	// EnvAPIBaseURL is the sentinel for the bot API — always resolves.
	if got := cfg.ServiceURL(EnvAPIBaseURL); got != "http://bot.example" {
		t.Errorf("ServiceURL(EnvAPIBaseURL) = %q, want bot API base", got)
	}

	// A spec-declared env name with the value set: return that value.
	t.Setenv(EnvDocAPIBaseURL, "http://docs.example")
	if got := cfg.ServiceURL(EnvDocAPIBaseURL); got != "http://docs.example" {
		t.Errorf("ServiceURL(OCTO_DOC_API_URL) = %q, want the env value", got)
	}

	// A spec-declared env name that is UNSET: empty string, NOT bot base
	// (this is the fail-loud guarantee the html domain depends on).
	t.Setenv(EnvDocAPIBaseURL, "")
	if got := cfg.ServiceURL(EnvDocAPIBaseURL); got != "" {
		t.Errorf("ServiceURL(unset OCTO_DOC_API_URL) = %q, want empty (must not fall back to bot base)", got)
	}

	// An unknown well-formed env name behaves the same: env value or empty.
	if got := cfg.ServiceURL("OCTO_MISC_URL"); got != "" {
		t.Errorf("ServiceURL(unset OCTO_MISC_URL) = %q, want empty", got)
	}
}
