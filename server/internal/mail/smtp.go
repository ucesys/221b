package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// SMTP sends over SMTP: TLS from the start, STARTTLS (which the server
// must offer: the console never falls back to the clear), or in the clear
// to a relay that needs no sign-in. It signs in with PLAIN when a user
// name is set, which only ever crosses TLS.
type SMTP struct {
	// TLS, when set, is the TLS configuration to start from (tests give
	// their own roots); the server's name is always checked.
	TLS *tls.Config
}

// Send sends one message.
func (x SMTP) Send(ctx context.Context, srv Server, m Message) error {
	body, err := Build(m, time.Now())
	if err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	tcfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if x.TLS != nil {
		tcfg = x.TLS.Clone()
	}
	tcfg.ServerName = srv.Host
	d := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	if srv.Security == SecurityTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tcfg}).DialContext(ctx, "tcp", srv.Address())
	} else {
		conn, err = d.DialContext(ctx, "tcp", srv.Address())
	}
	if err != nil {
		return fmt.Errorf("the server %s could not be reached: %w", srv.Address(), dialReason(err))
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, srv.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("the server did not greet as SMTP: %w", err)
	}
	defer c.Close()
	if err := c.Hello("221b"); err != nil {
		return fmt.Errorf("the server refused the greeting: %w", err)
	}
	if srv.Security == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS; pick TLS for port 465, or none for a relay inside the network")
		}
		if err := c.StartTLS(tcfg); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if srv.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the server does not offer sign-in (AUTH); leave the user name empty, or check the port")
		}
		if err := c.Auth(smtp.PlainAuth("", srv.Username, srv.Password, srv.Host)); err != nil {
			return fmt.Errorf("the server refused the sign-in: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("the server refused the From address: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("the server refused the recipient: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("the server refused the message: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("the message could not be sent: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the server refused the message: %w", err)
	}
	_ = c.Quit()
	return nil
}

// dialReason says a connection's failure without the noise of its
// layers.
func dialReason(err error) error {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return errors.New("it did not answer in time (a network policy may block the port)")
	case strings.Contains(err.Error(), "connection refused"):
		return errors.New("the connection was refused")
	case strings.Contains(err.Error(), "no such host"):
		return errors.New("no such host")
	}
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return fmt.Errorf("its certificate is not trusted: %w", cv.Err)
	}
	return err
}

// header is a header's value as one line: no line breaks, words outside
// ASCII encoded.
func header(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	return mime.QEncoding.Encode("utf-8", v)
}

// Build is a message as SMTP sends it: plain text, and HTML beside it
// when there is any. The addresses are checked again here: a line break in
// one would add a header.
func Build(m Message, now time.Time) ([]byte, error) {
	for _, a := range []string{m.From, m.To} {
		if _, err := CheckAddress("address", a); err != nil {
			return nil, err
		}
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := m.From[strings.LastIndex(m.From, "@")+1:]
	var b bytes.Buffer
	h := textproto.MIMEHeader{}
	h.Set("From", "221B <"+m.From+">")
	h.Set("To", m.To)
	h.Set("Subject", header(m.Subject))
	h.Set("Date", now.UTC().Format(time.RFC1123Z))
	h.Set("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h.Set("MIME-Version", "1.0")
	// Out-of-office answers and the like are not sent back to a machine.
	h.Set("Auto-Submitted", "auto-generated")
	h.Set("X-Auto-Response-Suppress", "All")
	if m.HTML == "" {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		writeHeader(&b, h)
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	mw := multipart.NewWriter(&b)
	h.Set("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	// The header goes before the parts the writer adds.
	var head bytes.Buffer
	writeHeader(&head, h)
	for _, part := range []struct{ ctype, text string }{{"text/plain; charset=utf-8", m.Text}, {"text/html; charset=utf-8", m.HTML}} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.ctype}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.text); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append(head.Bytes(), b.Bytes()...), nil
}

var headerOrder = []string{"From", "To", "Subject", "Date", "Message-Id", "Mime-Version", "Auto-Submitted", "X-Auto-Response-Suppress", "Content-Type", "Content-Transfer-Encoding"}

func writeHeader(b *bytes.Buffer, h textproto.MIMEHeader) {
	for _, k := range headerOrder {
		if v := h.Get(k); v != "" {
			fmt.Fprintf(b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
}

func writeQP(w interface{ Write([]byte) (int, error) }, text string) error {
	q := quotedprintable.NewWriter(w)
	if _, err := q.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return q.Close()
}
