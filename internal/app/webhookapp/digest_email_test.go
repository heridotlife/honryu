package webhookapp_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heridotlife/honryu/internal/ports/fake"
)

// smtpFake is a scripted SMTP relay: enough of the protocol for net/smtp to
// run a full transaction against -- greeting, EHLO with a chosen extension
// set, optional STARTTLS (with a real self-signed cert upgrade), AUTH, MAIL
// FROM / RCPT TO, DATA, QUIT -- recording everything a test wants to
// assert on. STARTTLS is offered only when offerSTARTTLS is set: the
// transport's plaintext-refusal test needs a relay that genuinely does not
// speak it.
type smtpFake struct {
	ln            net.Listener
	offerSTARTTLS bool
	cert          tls.Certificate

	mu      sync.Mutex
	conns   int
	authed  bool
	starttl bool
	from    string
	rcpts   []string
	data    string
	quit    bool
}

func newSMTPFake(t *testing.T, offerSTARTTLS bool) *smtpFake {
	t.Helper()
	f := &smtpFake{offerSTARTTLS: offerSTARTTLS}
	if offerSTARTTLS {
		f.cert = selfSignedCert(t)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtp fake listen: %v", err)
	}
	f.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *smtpFake) addr() string { return f.ln.Addr().String() }

func (f *smtpFake) snapshot() (conns int, authed, starttl, quit bool, from string, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.authed, f.starttl, f.quit, f.from, append([]string(nil), f.rcpts...), f.data
}

func (f *smtpFake) serve(conn net.Conn) {
	f.mu.Lock()
	f.conns++
	f.mu.Unlock()
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	writeLine := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	writeLine("220 honryu.test ESMTP ready (fake)")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := line
		if i := strings.IndexByte(line, ' '); i >= 0 {
			verb = line[:i]
		}
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			writeLine("250-honryu.test greets you")
			if f.offerSTARTTLS {
				writeLine("250-STARTTLS")
			}
			writeLine("250-AUTH PLAIN")
			writeLine("250 8BITMIME")
		case "STARTTLS":
			writeLine("220 2.0.0 Ready to start TLS")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			f.mu.Lock()
			f.starttl = true
			f.mu.Unlock()
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
		case "AUTH":
			f.mu.Lock()
			f.authed = true
			f.mu.Unlock()
			writeLine("235 2.7.0 Accepted")
		case "MAIL":
			f.mu.Lock()
			f.from = line
			f.mu.Unlock()
			writeLine("250 2.1.0 OK")
		case "RCPT":
			f.mu.Lock()
			f.rcpts = append(f.rcpts, line)
			f.mu.Unlock()
			writeLine("250 2.1.0 OK")
		case "DATA":
			writeLine("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			writeLine("250 2.0.0 OK: queued as fake")
		case "QUIT":
			f.mu.Lock()
			f.quit = true
			f.mu.Unlock()
			writeLine("221 2.0.0 Bye")
			return
		default:
			writeLine("500 5.5.2 Error: command not recognized")
		}
	}
}

// selfSignedCert mints the one cert the STARTTLS fake needs: a throwaway
// keypair for 127.0.0.1, an hour of validity. The client side skips
// verification (WithEmailTLSConfig) -- what the test wants is a genuine TLS
// upgrade, not a chain the process happens to trust.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("cert key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"127.0.0.1"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("cert key marshal: %v", err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("cert keypair: %v", err)
	}
	return cert
}

// skipVerify is the STARTTLS client config for tests: a real TLS upgrade
// whose throwaway chain the process does not trust.
func skipVerify() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, ServerName: "127.0.0.1"}
}

// emailDigestPayload is the compact stored digest the email tests render;
// deliberately short so the pretty-printed assertions stay readable.
func emailDigestPayload() []byte {
	return []byte(`{"event":"report.digest","project_id":7,"period":"daily","window_start":"2026-09-16T00:00:00Z","window_end":"2026-09-17T00:00:00Z","runs_total":4,"threshold_failures":1,"executions":[]}`)
}

// emailURL builds a digest SMTP URL against the fake. Empty username means
// no credentials in the URL; insecure toggles the explicit plaintext opt-in.
func emailURL(fakeAddr, username, query string) string {
	userinfo := ""
	if username != "" {
		userinfo = username + "@"
	}
	return fmt.Sprintf("smtp://%s%s?%s", userinfo, fakeAddr, query)
}

// TestDeliverDigestEmailOverSTARTTLSWithAuth: the full happy path -- a
// relay offering STARTTLS, credentials in the URL -- delivers one message:
// the connection is actually upgraded, auth happens, MAIL FROM and RCPT TO
// carry the configured addresses, and the DATA payload is the rendered
// digest mail. One confirmed relay is delivered, (true, nil).
func TestDeliverDigestEmailOverSTARTTLSWithAuth(t *testing.T) {
	relay := newSMTPFake(t, true)
	repo := fake.NewStore()

	svc := fast(t, repo).
		WithEmailTLSConfig(skipVerify()).
		WithEmailSink(emailURL(relay.addr(), "ops:s3cret",
			"from=honryu%40ops.example&to=ops@example.com,auditors@example.com"))
	delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Fatal("DeliverDigest = delivered=false, want true (the relay queued the message)")
	}

	conns, authed, starttls, quit, from, rcpts, data := relay.snapshot()
	if conns != 1 {
		t.Errorf("relay saw %d connections, want 1", conns)
	}
	if !starttls {
		t.Error("connection was never upgraded to TLS; the transport must require STARTTLS")
	}
	if !authed {
		t.Error("relay never saw AUTH; the URL carried credentials")
	}
	if want := "MAIL FROM:<honryu@ops.example>"; !strings.Contains(from, want) {
		t.Errorf("MAIL FROM line = %q, want it to carry %q", from, want)
	}
	if len(rcpts) != 2 {
		t.Fatalf("relay saw %d RCPT TO lines, want 2", len(rcpts))
	}
	if want := "RCPT TO:<ops@example.com>"; !strings.Contains(rcpts[0], want) {
		t.Errorf("first RCPT = %q, want %q", rcpts[0], want)
	}
	if want := "RCPT TO:<auditors@example.com>"; !strings.Contains(rcpts[1], want) {
		t.Errorf("second RCPT = %q, want %q", rcpts[1], want)
	}
	for _, want := range []string{
		"Subject: honryu digest 2026-09-17",
		"To: ops@example.com, auditors@example.com",
		"  \"runs_total\": 4",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message data %q missing %q", data, want)
		}
	}
	if !quit {
		t.Error("relay never saw QUIT; the transaction should end cleanly")
	}
}

// TestDeliverDigestEmailRequiresSTARTTLS: a relay that does not offer
// STARTTLS is refused -- plaintext is opt-in, and the refusal error names
// the allow_insecure=true override so the operator knows the way out. The
// failure is bounded retries (the shared delivery bounds), not one
// connection, and nothing is delivered.
func TestDeliverDigestEmailRequiresSTARTTLS(t *testing.T) {
	relay := newSMTPFake(t, false) // genuinely no STARTTLS extension
	repo := fake.NewStore()

	svc := fast(t, repo).WithEmailSink(emailURL(relay.addr(), "",
		"from=honryu%40ops.example&to=ops@example.com"))
	delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
	if err == nil {
		t.Fatal("DeliverDigest = nil error, want the plaintext refusal")
	}
	if !strings.Contains(err.Error(), "allow_insecure") {
		t.Errorf("refusal error %q does not name the allow_insecure override", err)
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing was sent)")
	}
	if conns, _, _, _, _, _, data := relay.snapshot(); conns != 3 {
		t.Errorf("relay saw %d connections, want the bounded 3", conns)
	} else if data != "" {
		t.Errorf("relay saw message data %q; nothing should be sent to a plaintext relay", data)
	}
}

// TestDeliverDigestEmailInsecureOverride: allow_insecure=true is the
// explicit opt-in -- the same relay that was refused above now receives the
// message over plaintext (no STARTTLS upgrade, no auth: the URL carried no
// credentials).
func TestDeliverDigestEmailInsecureOverride(t *testing.T) {
	relay := newSMTPFake(t, false)
	repo := fake.NewStore()

	svc := fast(t, repo).WithEmailSink(emailURL(relay.addr(), "",
		"from=honryu%40ops.example&to=ops@example.com&allow_insecure=true"))
	delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Fatal("DeliverDigest = delivered=false, want true (the insecure override honored)")
	}
	conns, authed, starttls, _, _, _, data := relay.snapshot()
	if conns != 1 || !strings.Contains(data, "Subject: honryu digest 2026-09-17") {
		t.Fatalf("relay got %d conns, data %q; want one queued message", conns, data)
	}
	if starttls {
		t.Error("relay reported a TLS upgrade; this fake never offers STARTTLS")
	}
	if authed {
		t.Error("relay saw AUTH without credentials in the URL")
	}
}

// TestWithEmailSinkRejectsBadURLs: a malformed SMTP URL never becomes a
// target -- the sink is refused (logged, credentials stripped) and the
// deployment behaves as unconfigured. A line break in an address is
// refused too: From:/To: are written from these values verbatim. The
// counting dialer is the proof: a refused sink never dials anything.
func TestWithEmailSinkRejectsBadURLs(t *testing.T) {
	var dials int
	repo := fake.NewStore()
	noDial := func(ctx context.Context, addr string) (net.Conn, error) {
		dials++
		return nil, net.ErrClosed
	}

	for name, raw := range map[string]string{
		"wrong scheme": "https://relay.example:2525?from=a@b.c&to=d@e.f",
		"no host":      "smtp://?from=a@b.c&to=d@e.f",
		"no from":      "smtp://relay.example:2525?to=d@e.f",
		"no to":        "smtp://relay.example:2525?from=a@b.c",
		"crlf in from": "smtp://relay.example:2525?from=a%0d%0aBcc:x@y&to=d@e.f",
		"garbage":      "::not a url",
	} {
		t.Run(name, func(t *testing.T) {
			dials = 0
			svc := fast(t, repo).WithEmailSink(raw).WithEmailDial(noDial)
			delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
			if err != nil {
				t.Fatalf("DeliverDigest: %v", err)
			}
			if delivered {
				t.Error("DeliverDigest = delivered=true, want false (no valid email sink was configured)")
			}
			if dials != 0 {
				t.Errorf("dialer saw %d calls, want 0 (the sink was rejected at wiring)", dials)
			}
		})
	}
}

// TestDeliverDigestEmailUnconfigured: no email sink configured is the
// unconfigured default -- the digest lane behaves exactly as before, and
// an empty URL is an explicit no-op, not an error.
func TestDeliverDigestEmailUnconfigured(t *testing.T) {
	relay := newSMTPFake(t, true)
	repo := fake.NewStore()

	svc := fast(t, repo).WithEmailSink("")
	delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing was configured)")
	}
	if conns, _, _, _, _, _, _ := relay.snapshot(); conns != 0 {
		t.Errorf("relay saw %d connections, want 0 (unconfigured)", conns)
	}
}

// TestDeliverDigestEmailMixedSuccess: a dead relay must not fail the digest
// when the other transports confirm -- the webhook's confirmation is enough
// for delivered, the house single-healthy-receiver rule.
func TestDeliverDigestEmailMixedSuccess(t *testing.T) {
	hook := newReceiver(200)
	defer hook.close()
	relay := newSMTPFake(t, false) // will be refused: no STARTTLS, no override
	repo := fake.NewStore()
	register(t, repo, 7, hook.url(), "", true)

	svc := fast(t, repo).WithEmailSink(emailURL(relay.addr(), "",
		"from=honryu%40ops.example&to=ops@example.com"))
	delivered, err := svc.DeliverDigest(context.Background(), 7, emailDigestPayload())
	if err != nil {
		t.Fatalf("DeliverDigest: %v", err)
	}
	if !delivered {
		t.Error("DeliverDigest = delivered=false, want true (the webhook confirmed)")
	}
}

// TestDeliverDigestEmailUnparseablePayloadFails: the email target was
// configured but the stored bytes cannot be rendered into a message -- the
// call must fail (not sit pending, not pass on a sibling), because a
// configured transport that can never confirm is a failure to report.
func TestDeliverDigestEmailUnparseablePayloadFails(t *testing.T) {
	relay := newSMTPFake(t, true)
	repo := fake.NewStore()

	svc := fast(t, repo).WithEmailSink(emailURL(relay.addr(), "",
		"from=honryu%40ops.example&to=ops@example.com"))
	delivered, err := svc.DeliverDigest(context.Background(), 7, []byte(`{not json`))
	if err == nil {
		t.Fatal("DeliverDigest = nil error, want the unrenderable-payload failure")
	}
	if delivered {
		t.Error("DeliverDigest = delivered=true, want false (nothing could be rendered)")
	}
	if conns, _, _, _, _, _, _ := relay.snapshot(); conns != 0 {
		t.Errorf("relay saw %d connections, want 0 (nothing was rendered to send)", conns)
	}
}
