package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"time"
)

// DefaultNotifier sends alerts via webhook HTTP POST and SMTP email.
type DefaultNotifier struct {
	smtpHost     string
	smtpPort     int
	smtpUsername string
	smtpPassword string
	smtpFrom     string
	httpClient   *http.Client
	logger       *slog.Logger
}

// NotifierConfig holds SMTP and HTTP settings for the notifier.
type NotifierConfig struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
}

// NewDefaultNotifier creates a notifier with the given SMTP configuration.
func NewDefaultNotifier(cfg NotifierConfig, logger *slog.Logger) *DefaultNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &DefaultNotifier{
		smtpHost:     cfg.SMTPHost,
		smtpPort:     cfg.SMTPPort,
		smtpUsername: cfg.SMTPUsername,
		smtpPassword: cfg.SMTPPassword,
		smtpFrom:     cfg.SMTPFrom,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: logger,
	}
}

// SendWebhook POSTs the alert payload as JSON to the given URL.
func (n *DefaultNotifier) SendWebhook(url string, payload AlertPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// SendEmail sends an alert email via SMTP. If SMTP is not configured, it logs instead.
func (n *DefaultNotifier) SendEmail(to, subject, body string) error {
	if n.smtpHost == "" {
		n.logger.Info("email notification (SMTP not configured)",
			"to", to,
			"subject", subject,
		)
		return nil
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		n.smtpFrom, to, subject, body)

	addr := fmt.Sprintf("%s:%d", n.smtpHost, n.smtpPort)
	var auth smtp.Auth
	if n.smtpUsername != "" {
		auth = smtp.PlainAuth("", n.smtpUsername, n.smtpPassword, n.smtpHost)
	}

	if err := smtp.SendMail(addr, auth, n.smtpFrom, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	return nil
}
