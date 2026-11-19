// Package mail builds and sends the emails Zynthera sends to users.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message is one email with a plain-text and an HTML version.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// ---------------------------------------------------------------- messages

// VerifyEmail is the message with the link that confirms an email address.
func VerifyEmail(webOrigin, to, token string) Message {
	link := webOrigin + "/verify-email?token=" + url.QueryEscape(token)
	return build(to, "Confirm your email for Zynthera",
		"Welcome to Zynthera. Confirm your email address to create your first PC.",
		"Confirm email", link,
		"This link works once and expires in 24 hours. If you did not create a Zynthera account, you can ignore this email.")
}

// ResetPassword is the message with the password reset link.
func ResetPassword(webOrigin, to, token string) Message {
	link := webOrigin + "/reset-password?token=" + url.QueryEscape(token)
	return build(to, "Reset your Zynthera password",
		"Someone asked to reset the password for this Zynthera account.",
		"Choose a new password", link,
		"This link works once and expires in 1 hour. If you did not ask for it, you can ignore this email and your password stays the same.")
}

var htmlTmpl = template.Must(template.New("mail").Parse(`<!doctype html>
<html lang="en">
<body style="margin:0;padding:24px;background:#f6f7fa;font-family:system-ui,-apple-system,Segoe UI,sans-serif;color:#141a26">
  <div style="max-width:480px;margin:0 auto;background:#ffffff;border:1px solid #dde2ec;border-radius:12px;padding:32px">
    <p style="margin:0 0 24px;font-size:18px;font-weight:600">Zynthera</p>
    <p style="margin:0 0 24px;font-size:15px;line-height:1.5">{{.Intro}}</p>
    <p style="margin:0 0 24px"><a href="{{.Link}}" style="display:inline-block;background:#2f5fe0;color:#ffffff;text-decoration:none;padding:10px 18px;border-radius:8px;font-size:15px;font-weight:600">{{.Button}}</a></p>
    <p style="margin:0 0 12px;font-size:13px;line-height:1.5;color:#5b6577">Or paste this link into your browser:<br>{{.Link}}</p>
    <p style="margin:0;font-size:13px;line-height:1.5;color:#5b6577">{{.Footer}}</p>
  </div>
</body>
</html>
`))

func build(to, subject, intro, button, link, footer string) Message {
	text := intro + "\n\n" + button + ":\n" + link + "\n\n" + footer + "\n"
	var html bytes.Buffer
	// The template is fixed and the data are plain strings, so this cannot fail.
	_ = htmlTmpl.Execute(&html, map[string]string{"Intro": intro, "Button": button, "Link": link, "Footer": footer})
	return Message{To: to, Subject: subject, Text: text, HTML: html.String()}
}

// ---------------------------------------------------------------- SMTP

// SMTPSender sends mail through an SMTP server.
type SMTPSender struct {
	Host string
	Port int
	User string
	Pass string
	From string // "Name <address>" or a bare address
}

// Send implements Sender.
func (s SMTPSender) Send(ctx context.Context, msg Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("mail: invalid From address: %w", err)
	}
	to, err := mail.ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("mail: invalid To address: %w", err)
	}
	body, err := encode(from, to, msg)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: connect to %s: %w", addr, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(nil); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if s.User != "" {
		// PlainAuth refuses to send the password over an unencrypted
		// connection, except to localhost.
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: finish body: %w", err)
	}
	return c.Quit()
}

func encode(from, to *mail.Address, msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, fmt.Errorf("mail: subject contains a line break")
	}
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("mail: random: %w", err)
	}
	boundary := "zyn-" + hex.EncodeToString(rnd[:])
	domain := "zynthera.local"
	if _, d, ok := strings.Cut(from.Address, "@"); ok {
		domain = d
	}

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(rnd[:])+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")

	part := func(contentType, content string) error {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + contentType + "; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
		qp := quotedprintable.NewWriter(&b)
		if _, err := qp.Write([]byte(content)); err != nil {
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		b.WriteString("\r\n")
		return nil
	}
	if err := part("text/plain", msg.Text); err != nil {
		return nil, fmt.Errorf("mail: encode text: %w", err)
	}
	if err := part("text/html", msg.HTML); err != nil {
		return nil, fmt.Errorf("mail: encode html: %w", err)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// ---------------------------------------------------------------- fake

// Fake records messages instead of sending them. It is used by tests.
type Fake struct {
	mu   sync.Mutex
	sent []Message
}

// Send implements Sender.
func (f *Fake) Send(_ context.Context, msg Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

// Sent returns a copy of everything sent so far.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Reset forgets all recorded messages.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
}
