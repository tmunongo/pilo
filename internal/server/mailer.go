package server

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strings"
	"sync"
)

type Mailer struct {
	mu           sync.RWMutex
	Host         string
	Port         string
	User         string
	Pass         string
	From         string
	LastResetURL string // useful for dev and tests
}

func NewMailerFromEnv() *Mailer {
	return &Mailer{
		Host: os.Getenv("SMTP_HOST"),
		Port: getenv("SMTP_PORT", "587"),
		User: os.Getenv("SMTP_USER"),
		Pass: os.Getenv("SMTP_PASS"),
		From: getenv("SMTP_FROM", "noreply@pilo.local"),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (m *Mailer) SendPasswordReset(toEmail, resetURL string) error {
	m.mu.Lock()
	m.LastResetURL = resetURL
	m.mu.Unlock()

	if m.Host == "" {
		log.Printf("[PILO AUTH] Password reset requested for %s: %s", toEmail, resetURL)
		return nil
	}

	addr := fmt.Sprintf("%s:%s", m.Host, m.Port)
	subject := "Reset your Pilo password"
	body := fmt.Sprintf("Hello,\n\nA password reset request was made for your Pilo account.\n\nClick the link below to set a new password:\n%s\n\nIf you did not request this, you can safely ignore this email.\n", resetURL)

	msg := strings.Join([]string{
		"From: " + m.From,
		"To: " + toEmail,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	var auth smtp.Auth
	if m.User != "" && m.Pass != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}

	if err := smtp.SendMail(addr, auth, m.From, []string{toEmail}, []byte(msg)); err != nil {
		log.Printf("[PILO AUTH] Failed to send reset email to %s: %v", toEmail, err)
		return err
	}

	return nil
}

