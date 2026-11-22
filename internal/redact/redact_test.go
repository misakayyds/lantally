package redact

import (
	"strings"
	"testing"
)

func TestStringMasksBearerAndSensitiveURLs(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		absent   []string
		contains []string
	}{
		{
			name:     "bearer token with symbols",
			input:    `request failed: Authorization: Bearer abc+def/123=xyz`,
			absent:   []string{"abc+def/123=xyz"},
			contains: []string{"Bearer [redacted]"},
		},
		{
			name:     "http url error with token query",
			input:    `Get "http://127.0.0.1:9090/version?token=sec-tok-123": dial tcp 127.0.0.1:9090: connection refused`,
			absent:   []string{"sec-tok-123"},
			contains: []string{"http://127.0.0.1:9090/version?token=[redacted]", "connection refused"},
		},
		{
			name:     "wss url with secret and userinfo",
			input:    `websocket dial wss://admin:pass999@127.0.0.1:9090/connections?secret=ws-sec-1: EOF`,
			absent:   []string{"pass999", "ws-sec-1"},
			contains: []string{"wss://admin:[redacted]@127.0.0.1:9090/connections?secret=[redacted]"},
		},
		{
			name:     "subscription url",
			input:    `fetch https://sub.example.test/api/v1/client/subscribe?flag=clash failed`,
			absent:   []string{"sub.example.test"},
			contains: []string{"[redacted-url]"},
		},
		{
			name:     "clean url unchanged",
			input:    `Get "http://127.0.0.1:9090/proxies": timeout`,
			contains: []string{"http://127.0.0.1:9090/proxies"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := String(tc.input)
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Fatalf("got %q, expected %q to be redacted", got, bad)
				}
			}
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Fatalf("got %q, expected substring %q", got, want)
				}
			}
		})
	}
}

func TestURLPreservesHostAndMasksSensitiveParts(t *testing.T) {
	got := URL("http://user:my-pass@203.0.113.10:9090/api?token=tok-123&mode=rule&api_key=k-999")
	if strings.Contains(got, "my-pass") || strings.Contains(got, "tok-123") || strings.Contains(got, "k-999") {
		t.Fatalf("URL leaked sensitive fields: %q", got)
	}
	for _, want := range []string{"http://user:[redacted]@203.0.113.10:9090/api", "token=[redacted]", "mode=rule", "api_key=[redacted]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("URL %q missing %q", got, want)
		}
	}
}

func TestSecretsScrubsRawAndEscapedLiterals(t *testing.T) {
	secret := "my/secret+val=1"
	in := `dial failed with token ` + secret + ` and escaped my%2Fsecret%2Bval%3D1`
	got := Secrets(in, secret, "s")
	if strings.Contains(got, secret) || strings.Contains(got, "my%2Fsecret%2Bval%3D1") {
		t.Fatalf("secret leaked in %q", got)
	}
	if !strings.Contains(got, "dial failed") {
		t.Fatalf("short secret 's' corrupted normal text: %q", got)
	}
}

func TestMalformedURLDoesNotLeakSecrets(t *testing.T) {
	badURL := "wss://admin:LeakPass111@127.0.0.1:badport?token=LeakTok222&secret=LeakQSec333"
	secrets := []string{"LeakPass111", "LeakTok222", "LeakQSec333"}

	gotURL := URL(badURL)
	for _, s := range secrets {
		if strings.Contains(gotURL, s) {
			t.Fatalf("URL(%q) leaked %q in %q", badURL, s, gotURL)
		}
	}
	if !strings.Contains(gotURL, "wss://admin:[redacted]@127.0.0.1:badport") ||
		!strings.Contains(gotURL, "token=[redacted]") ||
		!strings.Contains(gotURL, "secret=[redacted]") {
		t.Fatalf("URL(%q) unexpected sanitized output: %q", badURL, gotURL)
	}

	errMsg := `invalid url: parse "` + badURL + `": invalid port ":badport" after host`
	gotStr := String(errMsg)
	for _, s := range secrets {
		if strings.Contains(gotStr, s) {
			t.Fatalf("String(%q) leaked %q in %q", errMsg, s, gotStr)
		}
	}
	if !strings.Contains(gotStr, "wss://admin:[redacted]@127.0.0.1:badport") {
		t.Fatalf("String(%q) unexpected output: %q", errMsg, gotStr)
	}

	// User-only credentials and invalid percent-escapes also must not fail open.
	moreCases := []struct {
		input  string
		absent []string
	}{
		{
			input:  `parse "ws://SecretUserTok@127.0.0.1:badport?auth=AuthLeak444&api_key=ApiLeak555": invalid port`,
			absent: []string{"SecretUserTok", "AuthLeak444", "ApiLeak555"},
		},
		{
			input:  `http://127.0.0.1:9090/v1?token=BadEsc%ZZ&password=Pw999`,
			absent: []string{"BadEsc%ZZ", "Pw999"},
		},
	}
	for _, tc := range moreCases {
		outStr := String(tc.input)
		outURL := URL(tc.input)
		for _, bad := range tc.absent {
			if strings.Contains(outStr, bad) {
				t.Fatalf("String(%q) leaked %q in %q", tc.input, bad, outStr)
			}
			if strings.Contains(outURL, bad) {
				t.Fatalf("URL(%q) leaked %q in %q", tc.input, bad, outURL)
			}
		}
	}
}

func TestFallbackUserinfoAuthorityBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		rawURL  string
		wantURL string
		absent  []string
	}{
		{
			name:    "at sign in query without userinfo preserves host and email",
			rawURL:  "http://127.0.0.1:badport?email=a@b.com&token=LeakT",
			wantURL: "http://127.0.0.1:badport?email=a@b.com&token=[redacted]",
			absent:  []string{"LeakT"},
		},
		{
			name:    "unencoded at sign inside password redacts full password",
			rawURL:  "http://user:Leak@Pass@127.0.0.1:badport?token=LeakTok",
			wantURL: "http://user:[redacted]@127.0.0.1:badport?token=[redacted]",
			absent:  []string{"Leak", "Pass", "LeakTok"},
		},
		{
			name:    "user-only with at sign in query stops authority at question mark",
			rawURL:  "http://SecretUser@127.0.0.1:badport?email=a@b.com&token=LeakT",
			wantURL: "http://[redacted]@127.0.0.1:badport?email=a@b.com&token=[redacted]",
			absent:  []string{"SecretUser", "LeakT"},
		},
		{
			name:    "userinfo with path and at sign in query",
			rawURL:  "http://user:SecretPass@127.0.0.1:badport/path?email=a@b.com&token=LeakT",
			wantURL: "http://user:[redacted]@127.0.0.1:badport/path?email=a@b.com&token=[redacted]",
			absent:  []string{"SecretPass", "LeakT"},
		},
		{
			name:    "at sign in fragment without userinfo is preserved",
			rawURL:  "http://127.0.0.1:badport#user@host",
			wantURL: "http://127.0.0.1:badport#user@host",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotURL := URL(tc.rawURL)
			if gotURL != tc.wantURL {
				t.Fatalf("URL(%q) = %q, want %q", tc.rawURL, gotURL, tc.wantURL)
			}
			for _, bad := range tc.absent {
				if strings.Contains(gotURL, bad) {
					t.Fatalf("URL(%q) leaked %q in %q", tc.rawURL, bad, gotURL)
				}
			}

			errMsg := `parse "` + tc.rawURL + `": invalid port`
			wantErrMsg := `parse "` + tc.wantURL + `": invalid port`
			gotStr := String(errMsg)
			if gotStr != wantErrMsg {
				t.Fatalf("String(%q) = %q, want %q", errMsg, gotStr, wantErrMsg)
			}
			for _, bad := range tc.absent {
				if strings.Contains(gotStr, bad) {
					t.Fatalf("String(%q) leaked %q in %q", errMsg, bad, gotStr)
				}
			}
		})
	}
}
