package handlers

import "testing"

// TestBearerToken pins the Authorization-header parsing the API-key middleware
// relies on: the scheme is case-insensitive, surrounding space is trimmed, and
// anything malformed (or a non-Bearer scheme) yields ok=false.
func TestBearerToken(t *testing.T) {
	cases := []struct {
		header    string
		wantToken string
		wantOK    bool
	}{
		{"Bearer dk_abc", "dk_abc", true},
		{"bearer dk_abc", "dk_abc", true},     // scheme is case-insensitive
		{"BEARER dk_abc", "dk_abc", true},     // ...in any case
		{"Bearer   dk_abc  ", "dk_abc", true}, // trims surrounding whitespace
		{"", "", false},                       // no header
		{"dk_abc", "", false},                 // no scheme
		{"Basic abc", "", false},              // wrong scheme
		{"Bearer ", "", false},                // empty token
		{"Bearer", "", false},                 // too short to carry a token
	}
	for _, c := range cases {
		tok, ok := bearerToken(c.header)
		if ok != c.wantOK || tok != c.wantToken {
			t.Errorf("bearerToken(%q) = (%q, %v), want (%q, %v)", c.header, tok, ok, c.wantToken, c.wantOK)
		}
	}
}

// TestValidEmail pins the minimal signup/login email check. It is intentionally
// loose (a real check is sending mail); these cases only guard the obvious junk.
func TestValidEmail(t *testing.T) {
	valid := []string{"a@b.co", "user.name@example.com", "x@y.z"}
	invalid := []string{"", "no-at", "@nodomain.com", "user@", "user@nodot", "user@ space.com"}
	for _, e := range valid {
		if !validEmail(e) {
			t.Errorf("validEmail(%q) = false, want true", e)
		}
	}
	for _, e := range invalid {
		if validEmail(e) {
			t.Errorf("validEmail(%q) = true, want false", e)
		}
	}
}
