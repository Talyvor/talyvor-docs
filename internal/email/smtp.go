// Package email sends Docs' notification mail: an outbox row per message, written when the event happens and
// sent by a worker that also replays whatever was pending when the process last stopped.
package email

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP sends one message per call. Port 465 speaks TLS from the first byte (Cloudflare offers no STARTTLS);
// any other port upgrades with STARTTLS when the server offers it.
type SMTP struct {
	Host, User, Password, From string
	Port                       int
}

func (s SMTP) Send(ctx context.Context, b Brand, to, subject, body string) error {
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if s.Port == 465 {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: s.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && s.Port != 465 {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return err
		}
	} else if s.Port != 465 && !isLoopback(s.Host) {
		// A stripped STARTTLS offer must not turn into mail sent in the clear.
		return fmt.Errorf("email: %s offers no STARTTLS", s.Host)
	}
	if s.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return err
		}
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return err
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	// A branded workspace's name is only the display name; the address stays Docs' own, for SPF and DKIM.
	fromHeader := s.From
	if b.Name != "" {
		fromHeader = (&mail.Address{Name: b.Name + " via Talyvor", Address: from.Address}).String()
	}
	if _, err := w.Write(message(fromHeader, to, subject, body, b)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func message(from, to, subject, body string, brand Brand) []byte {
	// A random boundary, so no page title can close a part early.
	boundary := "talyvor-" + rand.Text()
	crlf := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n") }
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n", from, to,
		mimeHeader(subject), time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n")
	b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(crlf(body) + "\r\n")
	b.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(crlf(render(brand, body)) + "\r\n--" + boundary + "--\r\n")
	return []byte(b.String())
}

// mimeHeader encodes a subject that carries a page title: no line breaks reach the header, and non-ASCII
// is Q-encoded.
func mimeHeader(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return mime.QEncoding.Encode("utf-8", s)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
