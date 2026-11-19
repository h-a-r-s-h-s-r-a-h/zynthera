package mail

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"
)

func TestVerifyEmailMessage(t *testing.T) {
	msg := VerifyEmail("https://app.example.com", "alex@example.com", "tok_en-123")
	link := "https://app.example.com/verify-email?token=tok_en-123"
	if !strings.Contains(msg.Text, link) {
		t.Errorf("text part lacks the link:\n%s", msg.Text)
	}
	if !strings.Contains(msg.HTML, `href="`+link+`"`) {
		t.Errorf("html part lacks the link:\n%s", msg.HTML)
	}
	if msg.To != "alex@example.com" || msg.Subject == "" {
		t.Errorf("unexpected envelope: %+v", msg)
	}
}

func TestResetPasswordMessage(t *testing.T) {
	msg := ResetPassword("http://localhost:3000", "alex@example.com", "abc")
	if !strings.Contains(msg.Text, "http://localhost:3000/reset-password?token=abc") {
		t.Errorf("text part lacks the link:\n%s", msg.Text)
	}
}

func TestEncodeProducesAParsableMessage(t *testing.T) {
	from, _ := netmail.ParseAddress("Zynthera <no-reply@zynthera.local>")
	to, _ := netmail.ParseAddress("alex@example.com")
	msg := VerifyEmail("https://app.example.com", "alex@example.com", "tok")

	raw, err := encode(from, to, msg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Header.Get("To"); !strings.Contains(got, "alex@example.com") {
		t.Errorf("To = %q", got)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content type = %q, err = %v", mediaType, err)
	}

	mr := multipart.NewReader(parsed.Body, params["boundary"])
	var types []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, part.Header.Get("Content-Type"))
		// multipart.Reader decodes quoted-printable by itself only when the
		// header is hidden; decode explicitly to be independent of that.
		body, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "https://app.example.com/verify-email?token=tok") {
			t.Errorf("part %q lacks the link", part.Header.Get("Content-Type"))
		}
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("parts = %v", types)
	}
}

func TestEncodeRejectsHeaderInjection(t *testing.T) {
	from, _ := netmail.ParseAddress("no-reply@zynthera.local")
	to, _ := netmail.ParseAddress("alex@example.com")
	_, err := encode(from, to, Message{Subject: "Hi\r\nBcc: victim@example.com"})
	if err == nil {
		t.Fatal("subject with a line break was accepted")
	}
}
