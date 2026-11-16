// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// sendMail sends m to the addresses to over SMTP with the settings in
// opts: STARTTLS (required, the default), TLS from the start (port 465) or,
// only for a server on the same machine, none. A login (PLAIN or LOGIN) is
// only sent over TLS or to localhost; net/smtp refuses it otherwise.
func sendMail(ctx context.Context, opts map[string]string, to []string, m Message) error {
	host := strings.TrimSpace(opts["gamewe_notify_smtp_host"])
	if !hostName.MatchString(host) {
		return errors.New("e-mail: no valid SMTP server")
	}
	port := 587
	if v := strings.TrimSpace(opts["gamewe_notify_smtp_port"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("e-mail: no valid SMTP port")
		}
		port = n
	}
	security := strings.TrimSpace(opts["gamewe_notify_smtp_security"])
	if security == "" {
		security = "starttls"
	}
	if security != "starttls" && security != "tls" && security != "none" {
		return errors.New("e-mail: security must be starttls, tls or none")
	}
	from, err := address(strings.TrimSpace(opts["gamewe_notify_email_from"]))
	if err != nil {
		return errors.New("e-mail: no valid sender address")
	}
	name := strings.Trim(host, "[]")
	addr := net.JoinHostPort(name, strconv.Itoa(port))
	tlsConf := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if security == "tls" {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("e-mail: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, name)
	if err != nil {
		conn.Close()
		return fmt.Errorf("e-mail: %w", err)
	}
	defer c.Close()
	if security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("e-mail: the server offers no STARTTLS (choose tls for port 465)")
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return fmt.Errorf("e-mail: STARTTLS: %w", err)
		}
	}
	if user := strings.TrimSpace(opts["gamewe_notify_smtp_user"]); user != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			return errors.New("e-mail: the server offers no login (AUTH)")
		}
		pass := opts["gamewe_notify_smtp_password"]
		var a smtp.Auth
		switch {
		case hasWord(mechs, "PLAIN"):
			a = smtp.PlainAuth("", user, pass, name)
		case hasWord(mechs, "LOGIN"):
			a = &loginAuth{user: user, pass: pass, host: name}
		default:
			return fmt.Errorf("e-mail: no login method in common (server: %s)", mechs)
		}
		if err := c.Auth(a); err != nil {
			return fmt.Errorf("e-mail: login: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("e-mail: MAIL FROM: %w", err)
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("e-mail: RCPT TO %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("e-mail: DATA: %w", err)
	}
	if _, err := w.Write(mailText(from, m, time.Now())); err != nil {
		return fmt.Errorf("e-mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("e-mail: %w", err)
	}
	return c.Quit()
}

func hasWord(list, w string) bool {
	for _, f := range strings.Fields(strings.ToUpper(list)) {
		if f == w {
			return true
		}
	}
	return false
}

// mailText is the message: UTF-8 subject (RFC 2047), the recipients
// hidden from each other (each one is only in the envelope), the body as
// quoted-printable plain text.
func mailText(from string, m Message, now time.Time) []byte {
	id := make([]byte, 12)
	rand.Read(id)
	domain := from[strings.LastIndex(from, "@")+1:]
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from)
	h("To", "undisclosed-recipients:;")
	h("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Title)))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	q := quotedprintable.NewWriter(&b)
	q.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n")))
	q.Close()
	return b.Bytes()
}

// oneLine keeps a header value on one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// loginAuth is AUTH LOGIN (servers without PLAIN), only over TLS or to
// localhost like smtp.PlainAuth.
type loginAuth struct{ user, pass, host string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && server.Name != "localhost" && server.Name != "127.0.0.1" && server.Name != "::1" {
		return "", nil, errors.New("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(from))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}
