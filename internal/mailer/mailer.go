// Package mailer sends transactional email over SMTP. It supports implicit TLS
// (port 465) and STARTTLS (587/25), with or without auth.
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// smtpTimeout bounds every network phase of a send (dial + the whole SMTP
// conversation). Without it a black-holed or unreachable SMTP host would block the
// calling request goroutine — and leak the TCP socket — until the OS TCP timeout
// (minutes). The HTTP middleware's request timeout cannot interrupt these blocking
// net/smtp calls, so the deadline must live here.
const smtpTimeout = 20 * time.Second

// ErrDeliveryUncertain means DATA was written, but the acceptance reply was
// lost. Automatic callers must not blindly resend an email that may be queued.
var ErrDeliveryUncertain = errors.New("SMTP DATA acceptance is uncertain")

type Mailer struct {
	Host     string
	Port     string
	User     string
	Pass     string
	From     string
	FromName string
	Security string // "ssl" | "starttls" | "none"
}

func (m *Mailer) security() string {
	if m.Security != "" {
		return m.Security
	}
	if m.Port == "465" {
		return "ssl"
	}
	return "starttls"
}

func (m *Mailer) Send(to []string, subject, htmlBody string) error {
	if m.Host == "" {
		return fmt.Errorf("mailer: SMTP host not configured")
	}
	addr := net.JoinHostPort(m.Host, m.Port)
	msg := buildMessage(m.From, m.FromName, to, subject, htmlBody)

	var auth smtp.Auth
	if m.User != "" {
		auth = smtp.PlainAuth("", m.User, m.Pass, m.Host)
	}

	if m.security() == "ssl" {
		return m.sendImplicitTLS(addr, auth, to, msg)
	}
	return m.sendStartTLS(addr, auth, to, msg)
}

// dial opens a timeout-bounded TCP connection and arms an absolute deadline
// covering the entire subsequent SMTP conversation.
func (m *Mailer) dial(addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: smtpTimeout}).Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(smtpTimeout))
	return conn, nil
}

func (m *Mailer) sendImplicitTLS(addr string, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := m.dial(addr)
	if err != nil {
		return err
	}
	tconn := tls.Client(conn, &tls.Config{ServerName: m.Host})
	c, err := smtp.NewClient(tconn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	return deliver(c, auth, m.From, to, msg)
}

func (m *Mailer) sendStartTLS(addr string, auth smtp.Auth, to []string, msg []byte) error {
	conn, err := m.dial(addr)
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	// Hard failure when the server doesn't offer STARTTLS. Continuing on the
	// plaintext connection is a silent downgrade: an on-path attacker only has
	// to strip "250-STARTTLS" from the EHLO reply. With SMTP auth configured
	// this merely fails noisily (Go's PlainAuth refuses an unencrypted
	// connection), but with no auth configured the message itself goes out in
	// the clear — and these messages carry password-reset links.
	ok, _ := c.Extension("STARTTLS")
	if !ok {
		c.Close()
		return fmt.Errorf("SMTP 服务器 %s 未提供 STARTTLS；已中止发送以避免明文传输（如该服务器使用隐式 TLS，请把加密方式改为 SSL/TLS）", m.Host)
	}
	if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
		c.Close()
		return err
	}
	return deliver(c, auth, m.From, to, msg)
}

// deliver runs the AUTH→MAIL→RCPT→DATA→QUIT phase on an established client and
// always closes it. auth is applied only when the server advertises AUTH.
func deliver(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	defer c.Close()
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		var rejection *textproto.Error
		if errors.As(err, &rejection) && rejection.Code >= 400 {
			return err
		}
		return fmt.Errorf("%w: %v", ErrDeliveryUncertain, err)
	}
	// DATA's final 250 is the delivery acceptance boundary. QUIT is only
	// connection cleanup; its failure must never re-arm a business notification.
	_ = c.Quit()
	return nil
}

func buildMessage(fromAddr, fromName string, to []string, subject, htmlBody string) []byte {
	from := fromAddr
	if fromName != "" {
		from = mime.QEncoding.Encode("utf-8", fromName) + " <" + fromAddr + ">"
	}
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}
