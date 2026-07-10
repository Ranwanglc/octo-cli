package service

// End-to-end tests for the spec-driven html domain (octo-docs-html backend).
// These pin the reviewer's blocker fixes for OCT-153:
//   - html list actually hits OCTO_DOC_API_URL (not OCTO_API_BASE_URL)
//   - Authorization: Bearer <OCTO_DOC_WRITE_TOKEN> (not OCTO_BOT_TOKEN)
//   - fail-loud errors when OCTO_DOC_* env vars are missing, hint pointing at
//     the correct env name — the "no fallback to bot base" guarantee
//   - AUTH_REQUIRED (docs-html envelope code) maps to auth_error + exit code 3
//
// The bot chain is deliberately empty in every case to prove the html domain
// does not depend on OCTO_BOT_TOKEN at all.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/config"
	"github.com/Mininglamp-OSS/octo-cli/internal/credential"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
	"github.com/Mininglamp-OSS/octo-cli/internal/registry"
)

// rootWithHTMLService wires a bot-tokenless Factory (only OCTO_DOC_* env vars
// carry auth). The Client hook returns an error so any accidental fallback to
// f.Client() would fail loudly instead of silently borrowing bot state.
func rootWithHTMLService(t *testing.T, handler http.HandlerFunc) (*cobra.Command, *cmdutil.TestFactory, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	tf := cmdutil.NewTestFactory()
	// Bot base URL is set to a bogus value — html domain must NOT route here.
	// Any request that lands on this URL is a routing bug.
	tf.SetConfig(&config.Config{
		APIBaseURL: "http://bot-should-not-be-hit.invalid",
		Format:     "json",
	})
	// No credential: html domain does not consult the bot chain.
	tf.CredentialFunc = func() (*credential.BotCredential, error) {
		return nil, credential.ErrNoCredential
	}
	// f.Client() must not be called for html leaves. If it is, fail loudly.
	tf.ClientFunc = func() (*client.Client, error) {
		return nil, stringErr("bug: html leaf must not resolve through f.Client()")
	}
	tf.RegistryFunc = registry.MustNew

	root := &cobra.Command{Use: "octo-cli", SilenceUsage: true, SilenceErrors: true}
	RegisterServiceCommands(root, tf.Factory)
	return root, tf, srv
}

type stringErr string

func (e stringErr) Error() string { return string(e) }

// TestHTMLList_HappyPath_UsesDocEnvsAndBearer proves:
//  1. `octo-cli html list` requests OCTO_DOC_API_URL (not OCTO_API_BASE_URL)
//  2. Authorization is Bearer <OCTO_DOC_WRITE_TOKEN> (not OCTO_BOT_TOKEN)
//  3. --page / --page_size land on the wire
//  4. backend {data, pagination} envelope flattens into the CLI success shape
//     via output.WriteSuccess' splitPagination.
func TestHTMLList_HappyPath_UsesDocEnvsAndBearer(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	body := `{
	  "data": [
	    {"slug": "welcome", "title": "Welcome", "latest": "v1", "updated": "2026-07-10T00:00:00Z"},
	    {"slug": "faq", "title": "FAQ", "latest": "v3", "updated": "2026-07-09T00:00:00Z"}
	  ],
	  "pagination": {"total": 42, "page": 1, "page_size": 20}
	}`
	root, tf, srv := rootWithHTMLService(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	})
	// Set the docs env vars — no bot token anywhere.
	t.Setenv("OCTO_DOC_API_URL", srv.URL)
	t.Setenv("OCTO_DOC_WRITE_TOKEN", "doc_secret_abc")
	t.Setenv(config.EnvBotToken, "")

	root.SetArgs([]string{"html", "list", "--page", "1", "--page-size", "20"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("html list: %v; stderr=%s", err, tf.ErrOut.String())
	}

	if gotPath != "/v1/docs" {
		t.Errorf("request path = %q, want /v1/docs", gotPath)
	}
	if !strings.Contains(gotQuery, "page=1") || !strings.Contains(gotQuery, "page_size=20") {
		t.Errorf("request query = %q, want page=1 and page_size=20", gotQuery)
	}
	if gotAuth != "Bearer doc_secret_abc" {
		t.Errorf("Authorization = %q, want Bearer <OCTO_DOC_WRITE_TOKEN>", gotAuth)
	}

	// Envelope shape: backend {data, pagination} should flatten into
	// {ok, identity, data, _pagination}. Explicitly verify that so future
	// refactors of the pagination splitter can't silently drop the block.
	var env map[string]any
	if err := json.Unmarshal(tf.Out.Bytes(), &env); err != nil {
		t.Fatalf("envelope parse: %v\n%s", err, tf.Out.String())
	}
	if env["ok"] != true {
		t.Errorf("envelope ok = %v, want true", env["ok"])
	}
	data, _ := env["data"].([]any)
	if len(data) != 2 {
		t.Errorf("data entries = %d, want 2", len(data))
	}
	pag, _ := env["_pagination"].(map[string]any)
	if pag == nil {
		t.Errorf("envelope missing _pagination: %s", tf.Out.String())
	} else if pag["total"] != float64(42) {
		t.Errorf("_pagination.total = %v, want 42", pag["total"])
	}
}

// TestHTMLList_MissingDocToken_FailsWithDocEnvHint is the reviewer's blocker
// #1 defence: with OCTO_DOC_WRITE_TOKEN unset the op-layer auth check must
// fire — and the error must point at OCTO_DOC_WRITE_TOKEN, NEVER fall back to
// OCTO_BOT_TOKEN (even though OCTO_BOT_TOKEN is deliberately set here).
func TestHTMLList_MissingDocToken_FailsWithDocEnvHint(t *testing.T) {
	root, tf, srv := rootWithHTMLService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit when auth check fails; path=%s", r.URL.Path)
	})
	t.Setenv("OCTO_DOC_API_URL", srv.URL)
	t.Setenv("OCTO_DOC_WRITE_TOKEN", "") // the whole point
	// A bot token IS set — proving the fallback footgun does not fire.
	t.Setenv(config.EnvBotToken, "app_bot_should_not_be_used")

	root.SetArgs([]string{"html", "list"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error; stdout=%s", tf.Out.String())
	}
	ee := output.AsExitError(err)
	if ee == nil {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if ee.Type != "auth_error" {
		t.Errorf("error type = %q, want auth_error", ee.Type)
	}
	if ee.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", ee.ExitCode())
	}
	if !strings.Contains(ee.Message, "OCTO_DOC_WRITE_TOKEN") {
		t.Errorf("message = %q, want it to name OCTO_DOC_WRITE_TOKEN", ee.Message)
	}
	if strings.Contains(ee.Message, "OCTO_BOT_TOKEN") ||
		strings.Contains(ee.Hint, "OCTO_BOT_TOKEN") {
		t.Errorf("error must not mention OCTO_BOT_TOKEN (message=%q hint=%q)", ee.Message, ee.Hint)
	}
}

// TestHTMLList_MissingDocBaseURL_HintPointsAtDocEnv is the reviewer's blocker
// #2 defence: with OCTO_DOC_API_URL unset ServiceURL returns empty and
// client.Do must fail loud with a hint pointing at OCTO_DOC_API_URL — not
// OCTO_API_BASE_URL (which would silently misdirect the fix).
func TestHTMLList_MissingDocBaseURL_HintPointsAtDocEnv(t *testing.T) {
	root, tf, _ := rootWithHTMLService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server must not be hit when base URL is missing; path=%s", r.URL.Path)
	})
	t.Setenv("OCTO_DOC_API_URL", "") // the whole point
	t.Setenv("OCTO_DOC_WRITE_TOKEN", "doc_secret_abc")

	root.SetArgs([]string{"html", "list"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error; stdout=%s", tf.Out.String())
	}
	ee := output.AsExitError(err)
	if ee == nil {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if !strings.Contains(ee.Hint, "OCTO_DOC_API_URL") {
		t.Errorf("hint = %q, want it to name OCTO_DOC_API_URL", ee.Hint)
	}
	if strings.Contains(ee.Hint, config.EnvAPIBaseURL) {
		t.Errorf("hint must not name OCTO_API_BASE_URL (would misdirect); got %q", ee.Hint)
	}
}

// TestHTMLList_AuthRequiredMapping pins the errors.go +2 mapping: docs-html
// backend returns {"error":{"code":"AUTH_REQUIRED"}} on 401 — must classify
// as auth_error (exit 3), not fall through to the generic 401 path.
func TestHTMLList_AuthRequiredMapping(t *testing.T) {
	root, tf, srv := rootWithHTMLService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"AUTH_REQUIRED","message":"token missing"}}`))
	})
	t.Setenv("OCTO_DOC_API_URL", srv.URL)
	t.Setenv("OCTO_DOC_WRITE_TOKEN", "doc_secret_abc")

	root.SetArgs([]string{"html", "list"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("expected error; stdout=%s", tf.Out.String())
	}
	ee := output.AsExitError(err)
	if ee == nil {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if ee.Type != "auth_error" {
		t.Errorf("error type = %q, want auth_error", ee.Type)
	}
	if ee.ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", ee.ExitCode())
	}
	if ee.Code != "AUTH_REQUIRED" {
		t.Errorf("error code = %q, want AUTH_REQUIRED", ee.Code)
	}
	if !strings.Contains(ee.Hint, "OCTO_DOC_WRITE_TOKEN") {
		t.Errorf("hint = %q, want it to mention OCTO_DOC_WRITE_TOKEN", ee.Hint)
	}
}

// TestHTMLList_EmptyDataStillEmitsEnvelope covers the "data: []" edge case:
// the paginated envelope splitter must survive a zero-item response without
// nil-panicking, and _pagination must still be present.
func TestHTMLList_EmptyDataStillEmitsEnvelope(t *testing.T) {
	root, tf, srv := rootWithHTMLService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"data": [], "pagination": {"total": 0, "page": 1, "page_size": 20}}`))
	})
	t.Setenv("OCTO_DOC_API_URL", srv.URL)
	t.Setenv("OCTO_DOC_WRITE_TOKEN", "doc_secret_abc")

	root.SetArgs([]string{"html", "list"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("empty list: %v; stderr=%s", err, tf.ErrOut.String())
	}
	var env map[string]any
	if err := json.Unmarshal(tf.Out.Bytes(), &env); err != nil {
		t.Fatalf("envelope parse: %v", err)
	}
	if env["ok"] != true {
		t.Errorf("ok = %v", env["ok"])
	}
	data, _ := env["data"].([]any)
	if data == nil {
		t.Errorf("data must be [], not nil; envelope=%s", tf.Out.String())
	}
	if len(data) != 0 {
		t.Errorf("data length = %d, want 0", len(data))
	}
	if _, ok := env["_pagination"]; !ok {
		t.Errorf("_pagination missing from envelope: %s", tf.Out.String())
	}
}
