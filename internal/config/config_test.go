package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/talyvor/docs/internal/config"
)

// SEC-4 Layer 1 fail-closed: GATEWAY_AUTH_SECRET is the root of trust, so an unset or weak
// value must abort boot — never run with a forgeable /v1 boundary.
func TestLoad_GatewayAuthSecret_BootFailsClosed(t *testing.T) {
	t.Setenv("DOCS_DATABASE_URL", "postgres://x")

	t.Setenv("GATEWAY_AUTH_SECRET", "") // unset → fail
	if _, err := config.Load(); !errors.Is(err, config.ErrMissingEnv) {
		t.Fatalf("unset GATEWAY_AUTH_SECRET must fail Load, got %v", err)
	}

	t.Setenv("GATEWAY_AUTH_SECRET", "tooshort") // < 16 → fail
	if _, err := config.Load(); !errors.Is(err, config.ErrMissingEnv) {
		t.Fatalf("short GATEWAY_AUTH_SECRET must fail Load, got %v", err)
	}

	t.Setenv("GATEWAY_AUTH_SECRET", "a-strong-gateway-secret-0123456789") // >= 16 → ok
	if _, err := config.Load(); err != nil {
		t.Fatalf("valid GATEWAY_AUTH_SECRET must load: %v", err)
	}
}

// B28.248: DOCS_METRICS_TOKEN is optional (unset ⇒ /metrics 401s everyone), but a short one
// refuses to boot rather than guarding /metrics with a guessable token.
func TestLoad_MetricsToken(t *testing.T) {
	t.Setenv("DOCS_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", "a-strong-gateway-secret-0123456789")

	t.Setenv("DOCS_METRICS_TOKEN", "")
	if cfg, err := config.Load(); err != nil || cfg.MetricsToken != "" {
		t.Fatalf("unset DOCS_METRICS_TOKEN must load with no token, got %v / %q", err, cfg.MetricsToken)
	}

	t.Setenv("DOCS_METRICS_TOKEN", "tooshort")
	if _, err := config.Load(); !errors.Is(err, config.ErrMissingEnv) {
		t.Fatalf("short DOCS_METRICS_TOKEN must fail Load, got %v", err)
	}

	t.Setenv("DOCS_METRICS_TOKEN", "a-strong-scrape-token-0123456789")
	if cfg, err := config.Load(); err != nil || cfg.MetricsToken != "a-strong-scrape-token-0123456789" {
		t.Fatalf("valid DOCS_METRICS_TOKEN must load, got %v", err)
	}
}

func TestLoad_EmailSwitchedOnButIncompleteRefusesToBoot(t *testing.T) {
	t.Setenv("DOCS_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", "a-strong-gateway-secret-0123456789")
	t.Setenv("DOCS_EMAIL_ENABLED", "")
	if cfg, err := config.Load(); err != nil || cfg.EmailEnabled {
		t.Fatalf("email unset: err=%v enabled=%v, want booted with email off", err, cfg != nil && cfg.EmailEnabled)
	}
	t.Setenv("DOCS_EMAIL_ENABLED", "true")
	t.Setenv("DOCS_SMTP_PORT", "465")
	t.Setenv("DOCS_EMAIL_FROM", "Talyvor Docs <docs@talyvor.com>")
	t.Setenv("DOCS_SMTP_HOST", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DOCS_SMTP_HOST") {
		t.Fatalf("email on with no host: err=%v, want a refusal naming DOCS_SMTP_HOST", err)
	}
	t.Setenv("DOCS_SMTP_HOST", "smtp.mx.cloudflare.net")
	if cfg, err := config.Load(); err != nil || !cfg.EmailEnabled || cfg.SMTPPort != 465 {
		t.Fatalf("email on and complete: err=%v", err)
	}
}
