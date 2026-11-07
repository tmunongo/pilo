package server_test

import (
	"os"
	"strings"
	"testing"

	"pilo/internal/server"
)

func TestMailerEnvAndFallback(t *testing.T) {
	// 1. NewMailerFromEnv with empty SMTP_HOST defaults
	os.Unsetenv("SMTP_HOST")
	os.Unsetenv("SMTP_PORT")
	os.Unsetenv("SMTP_USER")
	os.Unsetenv("SMTP_PASS")
	os.Unsetenv("SMTP_FROM")

	m := server.NewMailerFromEnv()
	if m.Port != "587" {
		t.Errorf("expected default SMTP port 587, got %s", m.Port)
	}
	if m.From != "noreply@pilo.local" {
		t.Errorf("expected default SMTP from noreply@pilo.local, got %s", m.From)
	}

	// 2. Console fallback when Host is empty
	err := m.SendPasswordReset("test@example.com", "http://localhost:8080/reset-password?token=abc")
	if err != nil {
		t.Errorf("expected nil error in console fallback mode, got %v", err)
	}
	if m.LastResetURL != "http://localhost:8080/reset-password?token=abc" {
		t.Errorf("expected LastResetURL to be tracked, got %s", m.LastResetURL)
	}

	// 3. NewMailerFromEnv with custom env vars
	os.Setenv("SMTP_HOST", "smtp.example.com")
	os.Setenv("SMTP_PORT", "2525")
	os.Setenv("SMTP_USER", "smtpuser")
	os.Setenv("SMTP_PASS", "smtppass")
	os.Setenv("SMTP_FROM", "pilo@custom.domain")
	defer func() {
		os.Unsetenv("SMTP_HOST")
		os.Unsetenv("SMTP_PORT")
		os.Unsetenv("SMTP_USER")
		os.Unsetenv("SMTP_PASS")
		os.Unsetenv("SMTP_FROM")
	}()

	customM := server.NewMailerFromEnv()
	if customM.Host != "smtp.example.com" {
		t.Errorf("expected host smtp.example.com, got %s", customM.Host)
	}
	if customM.Port != "2525" {
		t.Errorf("expected port 2525, got %s", customM.Port)
	}
	if customM.User != "smtpuser" {
		t.Errorf("expected user smtpuser, got %s", customM.User)
	}
	if customM.Pass != "smtppass" {
		t.Errorf("expected pass smtppass, got %s", customM.Pass)
	}
	if customM.From != "pilo@custom.domain" {
		t.Errorf("expected from pilo@custom.domain, got %s", customM.From)
	}

	// Sending to invalid host should fail with connection error (proves it attempted real SMTP)
	err = customM.SendPasswordReset("user@example.com", "http://localhost:8080/reset-password?token=xyz")
	if err == nil {
		t.Errorf("expected error connecting to non-existent SMTP host")
	} else if !strings.Contains(err.Error(), "dial") && !strings.Contains(err.Error(), "lookup") && !strings.Contains(err.Error(), "connection") {
		t.Logf("got expected network error: %v", err)
	}
}
