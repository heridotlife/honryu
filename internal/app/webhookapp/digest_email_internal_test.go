package webhookapp

import (
	"strings"
	"testing"
	"time"
)

// TestParseEmailSink pins the URL decomposition on its own: the smtp scheme
// and host are required, from/to become the message's addresses, the port
// defaults to 587 (the STARTTLS submission port) when omitted, credentials
// come out of the userinfo, and allow_insecure=true is the only spelling of
// the plaintext opt-in.
func TestParseEmailSink(t *testing.T) {
	sink, err := parseEmailSink("smtp://ops:s3cret@relay.example?from=honryu@ops.example&to=ops@example.com,auditors@example.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sink.hostport != "relay.example:587" {
		t.Errorf("hostport = %q, want the default submission port 587", sink.hostport)
	}
	if sink.host != "relay.example" {
		t.Errorf("host = %q, want relay.example", sink.host)
	}
	if sink.username != "ops" || sink.password != "s3cret" {
		t.Errorf("credentials = %q/%q, want ops/s3cret", sink.username, sink.password)
	}
	if sink.from != "honryu@ops.example" {
		t.Errorf("from = %q, want honryu@ops.example", sink.from)
	}
	if len(sink.to) != 2 || sink.to[0] != "ops@example.com" || sink.to[1] != "auditors@example.com" {
		t.Errorf("to = %q, want both comma-separated recipients", sink.to)
	}
	if sink.insecure {
		t.Error("insecure = true, want false (no allow_insecure in the URL)")
	}

	sink, err = parseEmailSink("smtp://127.0.0.1:2525?from=a@b.c&to=d@e.f&allow_insecure=true")
	if err != nil {
		t.Fatalf("parse with port: %v", err)
	}
	if sink.hostport != "127.0.0.1:2525" || !sink.insecure {
		t.Errorf("hostport/insecure = %q/%v, want the explicit port and the plaintext opt-in", sink.hostport, sink.insecure)
	}

	for name, raw := range map[string]string{
		"wrong scheme": "https://relay.example?from=a@b.c&to=d@e.f",
		"no scheme":    "//relay.example?from=a@b.c&to=d@e.f",
		"no host":      "smtp://?from=a@b.c&to=d@e.f",
		"no from":      "smtp://relay.example?to=d@e.f",
		"no to":        "smtp://relay.example?from=a@b.c",
		"crlf in from": "smtp://relay.example?from=a%0d%0aBcc:x@y&to=d@e.f",
		"crlf in to":   "smtp://relay.example?from=a@b.c&to=d%0ae.f",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseEmailSink(raw); err == nil {
				t.Fatalf("parseEmailSink(%q) = nil error, want a refusal", raw)
			}
		})
	}
}

// TestRedactSMTPURL: log lines get scheme://host and nothing else -- the
// userinfo carries the relay password, the query carries addresses and the
// insecure flag.
func TestRedactSMTPURL(t *testing.T) {
	got := redactSMTPURL("smtp://ops:s3cret@relay.example:2525?from=a@b.c&to=d@e.f&allow_insecure=true")
	if got != "smtp://relay.example:2525" {
		t.Errorf("redactSMTPURL = %q, want smtp://relay.example:2525", got)
	}
	if got := redactSMTPURL("::not a url"); got != "(unparseable smtp url)" {
		t.Errorf("redactSMTPURL(unparseable) = %q, want the placeholder", got)
	}
}

// TestBuildDigestEmail pins the message formatting on its own: headers in
// wire order, the subject carrying the window's end date in UTC, recipients
// comma-joined into To:, and the body the stored JSON pretty printed -- the
// same data, spaced for a mail client, not a lossy re-rendering.
func TestBuildDigestEmail(t *testing.T) {
	sink, err := parseEmailSink("smtp://ops:s3cret@relay.example:2525?from=honryu%40ops.example&to=ops@example.com,auditors@example.com")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	now := time.Date(2026, 9, 17, 8, 30, 0, 0, time.UTC)
	body := []byte(`{"event":"report.digest","project_id":7,"period":"daily","window_start":"2026-09-16T00:00:00Z","window_end":"2026-09-17T00:00:00Z","runs_total":4,"threshold_failures":1,"executions":[]}`)
	msg, err := buildDigestEmail(sink, body, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	text := string(msg)
	for _, want := range []string{
		"From: honryu@ops.example\r\n",
		"To: ops@example.com, auditors@example.com\r\n",
		"Subject: honryu digest 2026-09-17\r\n",
		"Date: Thu, 17 Sep 2026 08:30:00 +0000\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n{",                 // headers end, body is JSON
		"\n  \"runs_total\": 4", // pretty printed, not the compact stored bytes
	} {
		if !strings.Contains(text, want) {
			t.Errorf("message %q missing %q", text, want)
		}
	}
	if strings.Contains(text, "s3cret") {
		t.Error("message contains the relay password; credentials are for the connection, not the mail")
	}
}

// TestBuildDigestEmailRejectsUnparseable: a payload that no longer parses
// is a build error (surfaced as a failed delivery by the caller), never an
// empty or half-rendered message.
func TestBuildDigestEmailRejectsUnparseable(t *testing.T) {
	sink := &emailSink{hostport: "relay.example:587", host: "relay.example", from: "a@b.c", to: []string{"d@e.f"}}
	if _, err := buildDigestEmail(sink, []byte(`{not json`), time.Now()); err == nil {
		t.Fatal("buildDigestEmail = nil error, want the parse failure")
	}
}
