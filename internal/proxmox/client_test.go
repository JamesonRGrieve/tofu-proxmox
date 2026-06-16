// SPDX-License-Identifier: AGPL-3.0-or-later

package proxmox

import (
	"net/http"
	"testing"
)

func TestSpecForAndAuthorization(t *testing.T) {
	cases := []struct {
		product       Product
		wantOK        bool
		wantPort      int
		wantCookie    string
		wantToken     bool
		wantAuthValue string // authorization("u@pam!t", "SECRET") when tokens supported
	}{
		{PVE, true, 8006, "PVEAuthCookie", true, "PVEAPIToken=u@pam!t=SECRET"},
		{PBS, true, 8007, "PBSAuthCookie", true, "PBSAPIToken=u@pam!t:SECRET"},
		{PMG, true, 8006, "PMGAuthCookie", false, ""},
		{PDM, true, 8443, "__Host-PDMAuthCookie", true, "PDMAPIToken=u@pam!t:SECRET"},
		{Product("nope"), false, 0, "", false, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.product), func(t *testing.T) {
			s, ok := specFor(tc.product)
			if ok != tc.wantOK {
				t.Fatalf("specFor(%q) ok=%v, want %v", tc.product, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if s.defaultPort != tc.wantPort {
				t.Errorf("defaultPort=%d, want %d", s.defaultPort, tc.wantPort)
			}
			if s.cookieName != tc.wantCookie {
				t.Errorf("cookieName=%q, want %q", s.cookieName, tc.wantCookie)
			}
			if s.supportsToken() != tc.wantToken {
				t.Errorf("supportsToken=%v, want %v", s.supportsToken(), tc.wantToken)
			}
			if tc.wantToken {
				if got := s.authorization("u@pam!t", "SECRET"); got != tc.wantAuthValue {
					t.Errorf("authorization=%q, want %q", got, tc.wantAuthValue)
				}
			}
		})
	}
}

func TestNewClientBaseURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"pve default port", Config{Product: PVE, Host: "203.0.113.2", TokenID: "u!t", TokenSecret: "s"}, "https://203.0.113.2:8006/api2/json"},
		{"pbs default port", Config{Product: PBS, Host: "203.0.113.3", TokenID: "u!t", TokenSecret: "s"}, "https://203.0.113.3:8007/api2/json"},
		{"explicit port wins", Config{Product: PVE, Host: "h", Port: 9000, TokenID: "u!t", TokenSecret: "s"}, "https://h:9000/api2/json"},
		{"host:port kept", Config{Product: PVE, Host: "h:7777", TokenID: "u!t", TokenSecret: "s"}, "https://h:7777/api2/json"},
		{"scheme stripped", Config{Product: PVE, Host: "https://h/", TokenID: "u!t", TokenSecret: "s"}, "https://h:8006/api2/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if c.base != tc.want {
				t.Errorf("base=%q, want %q", c.base, tc.want)
			}
		})
	}
}

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"unknown product", Config{Product: "nope", Host: "h", TokenID: "u!t", TokenSecret: "s"}},
		{"token on pmg rejected", Config{Product: PMG, Host: "h", TokenID: "u!t", TokenSecret: "s"}},
		{"no credentials", Config{Product: PVE, Host: "h"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.cfg); err == nil {
				t.Fatalf("NewClient(%+v) err=nil, want error", tc.cfg)
			}
		})
	}
}

func TestNewClientPMGTicketOK(t *testing.T) {
	if _, err := NewClient(Config{Product: PMG, Host: "h", Username: "root@pam", Password: "x"}); err != nil {
		t.Fatalf("PMG ticket auth should be allowed: %v", err)
	}
}

func TestNotFound(t *testing.T) {
	if !NotFound(&APIError{Status: http.StatusNotFound}) {
		t.Error("404 APIError should be NotFound")
	}
	if NotFound(&APIError{Status: http.StatusInternalServerError}) {
		t.Error("500 APIError should not be NotFound")
	}
	if NotFound(nil) {
		t.Error("nil should not be NotFound")
	}
}

func TestUnwrapData(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"object data", `{"data":{"cores":4,"memory":16384}}`, `{"cores":4,"memory":16384}`},
		{"string data (upid)", `{"data":"UPID:desktop:00001:..:..:vzcreate:108:root@pam:"}`, `"UPID:desktop:00001:..:..:vzcreate:108:root@pam:"`},
		{"null data", `{"data":null}`, `null`},
		{"no data key", `{"errors":{"x":"y"}}`, `{"errors":{"x":"y"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(unwrapData([]byte(tc.in))); got != tc.want {
				t.Errorf("unwrapData(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
