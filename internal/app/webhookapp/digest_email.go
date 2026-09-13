package webhookapp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// dialFunc opens the email transport's relay connection, bounded by the
// caller's per-attempt context. A named type so WithEmailDial's contract
// reads on the field, not in a doc comment.
type dialFunc func(ctx context.Context, addr string) (net.Conn, error)

// emailSink is a parsed HONRYU_DIGEST_SMTP_URL: one relay, one sender, the
// digest's recipients, and whether the deployment explicitly opted out of
// STARTTLS for this relay. The raw URL is never kept -- everything it
// carries worth logging is on these fields, and the password stays out of
// logs by construction.
type emailSink struct {
	// hostport is the dial address (host and port, the port defaulted to
	// 587 -- the STARTTLS submission port -- when the URL omitted it);
	// host is the bare hostname for EHLO, TLS verification, and auth.
	hostport string
	host     string
	username string
	password string
	from     string
	to       []string
	// insecure records an explicit allow_insecure=true: plaintext SMTP is
	// opt-in per relay, never a fallback.
	insecure bool
}

// parseEmailSink validates and decomposes a digest SMTP URL. Every failure
// names the part that was wrong -- the value fails at startup (config.Load)
// or at wiring (WithEmailSink), never mid-delivery as a surprise.
func parseEmailSink(raw string) (*emailSink, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("webhook: digest SMTP URL did not parse: %w", err)
	}
	if u.Scheme != "smtp" {
		return nil, fmt.Errorf("webhook: digest SMTP URL must use the smtp:// scheme, got %q", u.Scheme+"://")
	}
	if u.Host == "" {
		return nil, errors.New("webhook: digest SMTP URL needs a relay host")
	}
	from := strings.TrimSpace(u.Query().Get("from"))
	if from == "" {
		return nil, errors.New("webhook: digest SMTP URL needs a from= query parameter (the sender address)")
	}
	var to []string
	for _, addr := range strings.Split(u.Query().Get("to"), ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			to = append(to, addr)
		}
	}
	if len(to) == 0 {
		return nil, errors.New("webhook: digest SMTP URL needs a to= query parameter (the recipient addresses)")
	}
	// A line break in an address is header injection, not a typo: the From:
	// and To: headers are written from these values verbatim, so the guard
	// is here, at parse time, not in the mail body's blind trust.
	for _, addr := range append([]string{from}, to...) {
		if strings.ContainsAny(addr, "\r\n") {
			return nil, fmt.Errorf("webhook: digest SMTP address contains a line break: %q", addr)
		}
	}
	sink := &emailSink{
		host:     u.Hostname(),
		from:     from,
		to:       to,
		insecure: u.Query().Get("allow_insecure") == "true",
	}
	if port := u.Port(); port != "" {
		sink.hostport = net.JoinHostPort(sink.host, port)
	} else {
		// 587 is the submission port: the one that speaks STARTTLS.
		sink.hostport = net.JoinHostPort(sink.host, "587")
	}
	if u.User != nil {
		sink.username = u.User.Username()
		sink.password, _ = u.User.Password()
	}
	return sink, nil
}

// redactSMTPURL reduces an SMTP URL to scheme://host:port for log lines:
// the userinfo carries the relay's password and the query carries the
// addresses and the allow_insecure flag, and none of it belongs in a log.
func redactSMTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "(unparseable smtp url)"
	}
	return u.Scheme + "://" + u.Host
}

// buildDigestEmail wraps a stored digest payload as a plain email. The
// subject leads with the window's end date (UTC) -- the thing an operator
// scanning a mailbox triages by -- and the body is the stored JSON pretty
// printed (json.Indent, byte-for-byte the same data, just spaced out) so a
// mail client shows the exact wire contract rather than a lossy summary.
// Cannot inject headers: parseEmailSink already refused line breaks in the
// addresses, and the subject is all digits and dashes.
func buildDigestEmail(sink *emailSink, body []byte, now time.Time) ([]byte, error) {
	var v digestView
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("webhook: digest payload did not parse: %w", err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		// Unreachable after a successful Unmarshal, but the fallback is
		// free: the original bytes, unspaced, are still the same data.
		pretty.Reset()
		pretty.Write(body)
	}
	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: honryu digest %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		sink.from,
		strings.Join(sink.to, ", "),
		v.WindowEnd.UTC().Format("2006-01-02"),
		now.Format(time.RFC1123Z),
		pretty.String(),
	)
	return []byte(msg), nil
}

// deliverDigestEmail delivers one rendered digest message to the relay with
// the delivery lane's shared bounds: up to deliverAttempts tries, each
// bounded by the delivery timeout, backoff between tries. A relay that is
// down is exactly as much a notification problem as an HTTP receiver that
// is down -- retried briefly, then abandoned to the log, never a retry
// farm.
func (s *Service) deliverDigestEmail(ctx context.Context, sink *emailSink, msg []byte) error {
	var lastErr error
	for attempt := 1; attempt <= deliverAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.emailAttempt(ctx, sink, msg); err != nil {
			lastErr = err
			if attempt < deliverAttempts {
				select {
				case <-time.After(s.backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("webhook: %d attempts to relay %s all failed: %w", deliverAttempts, sink.hostport, lastErr)
}

// emailAttempt is one SMTP transaction: dial (under the attempt's timeout
// context, so a hung connect cannot eat the budget), EHLO, STARTTLS unless
// the deployment opted out, auth when the URL carried credentials, then the
// mail transaction. The deadline is pinned onto the raw conn as well --
// net/smtp has no context, so the conn's own deadline is what bounds the
// conversation once the dial returns.
func (s *Service) emailAttempt(ctx context.Context, sink *emailSink, msg []byte) error {
	attemptCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	conn, err := s.dial(attemptCtx, sink.hostport)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := attemptCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, sink.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("honryu"); err != nil {
		return err
	}
	if !sink.insecure {
		offers, _ := c.Extension("STARTTLS")
		if !offers {
			return fmt.Errorf("webhook: relay %s does not offer STARTTLS; refusing to send the digest in plaintext (add allow_insecure=true to the digest SMTP URL to override)", sink.hostport)
		}
		conf := s.emailTLS
		if conf == nil {
			conf = &tls.Config{ServerName: sink.host}
		}
		if err := c.StartTLS(conf); err != nil {
			return err
		}
	}
	if sink.username != "" {
		// PlainAuth itself refuses an unencrypted connection to anything
		// but localhost -- allow_insecure plus credentials dies here with
		// stdlib's own message, which is the documentation.
		if err := c.Auth(smtp.PlainAuth("", sink.username, sink.password, sink.host)); err != nil {
			return err
		}
	}
	if err := c.Mail(sink.from); err != nil {
		return err
	}
	for _, rcpt := range sink.to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
