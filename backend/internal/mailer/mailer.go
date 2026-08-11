// Package mailer isolates email delivery from account business rules.
package mailer

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/example/agent-platform/backend/internal/logger"
)

type Message struct {
	To      string
	Subject string
	Text    string
}

type Sender interface {
	Send(ctx context.Context, message Message) error
}

// LogSender is for local development only. It makes one-time links available
// in structured logs without pretending an email was delivered.
type LogSender struct{}

func (LogSender) Send(ctx context.Context, message Message) error {
	logger.Info(ctx, "development email generated",
		logger.String("email_to", message.To),
		logger.String("email_subject", message.Subject),
		logger.String("email_body", message.Text),
	)
	return nil
}

type NoopSender struct{}

func (NoopSender) Send(context.Context, Message) error { return nil }

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

type SMTPSender struct{ config SMTPConfig }

func NewSMTP(config SMTPConfig) (*SMTPSender, error) {
	if strings.TrimSpace(config.Host) == "" || config.Port <= 0 || strings.TrimSpace(config.From) == "" {
		return nil, fmt.Errorf("SMTP host, port and from address are required")
	}
	return &SMTPSender{config: config}, nil
}

func (s *SMTPSender) Send(_ context.Context, message Message) error {
	address := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	var authentication smtp.Auth
	if s.config.Username != "" {
		authentication = smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)
	}
	body := "From: " + s.config.From + "\r\n" +
		"To: " + message.To + "\r\n" +
		"Subject: " + message.Subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" + message.Text
	return smtp.SendMail(address, authentication, s.config.From, []string{message.To}, []byte(body))
}
