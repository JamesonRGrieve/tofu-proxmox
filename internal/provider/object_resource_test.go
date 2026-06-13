// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import "testing"

func TestSubsetMatches(t *testing.T) {
	cases := []struct {
		name        string
		prior, cfg  string
		wantMatched bool
	}{
		{
			name:        "config subset of full object — match (0-diff)",
			prior:       `{"vmid":108,"hostname":"sglang","cores":4,"memory":16384,"digest":"abc"}`,
			cfg:         `{"vmid":108,"hostname":"sglang"}`,
			wantMatched: true,
		},
		{
			name:        "declared key drifted — no match (update)",
			prior:       `{"vmid":108,"hostname":"old","cores":4}`,
			cfg:         `{"vmid":108,"hostname":"sglang"}`,
			wantMatched: false,
		},
		{
			name:        "declared key missing on device — no match",
			prior:       `{"vmid":108,"cores":4}`,
			cfg:         `{"vmid":108,"hostname":"sglang"}`,
			wantMatched: false,
		},
		{
			name:        "key order / whitespace insensitive — match",
			prior:       `{"hostname":"sglang","vmid":108}`,
			cfg:         "{\n  \"vmid\": 108,\n  \"hostname\": \"sglang\"\n}",
			wantMatched: true,
		},
		{
			name:        "transient device-only key (lock) ignored — match",
			prior:       `{"vmid":108,"hostname":"sglang","lock":"backup"}`,
			cfg:         `{"vmid":108,"hostname":"sglang"}`,
			wantMatched: true,
		},
		{
			name:        "nested object value compared structurally — match",
			prior:       `{"net0":{"bridge":"vmbr3","tag":5},"vmid":108}`,
			cfg:         `{"net0":{"tag":5,"bridge":"vmbr3"}}`,
			wantMatched: true,
		},
		{
			name:        "invalid prior JSON — no match (fall back to diff)",
			prior:       `not json`,
			cfg:         `{"a":1}`,
			wantMatched: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subsetMatches(tc.prior, tc.cfg); got != tc.wantMatched {
				t.Fatalf("subsetMatches() = %v, want %v", got, tc.wantMatched)
			}
		})
	}
}

func TestNormPath(t *testing.T) {
	for in, want := range map[string]string{
		"nodes/desktop/lxc/108":  "/nodes/desktop/lxc/108",
		"/nodes/desktop/lxc/108": "/nodes/desktop/lxc/108",
		" /access/users ":        "/access/users",
		"version":                "/version",
	} {
		if got := normPath(in); got != want {
			t.Errorf("normPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParentCollection(t *testing.T) {
	for in, want := range map[string]string{
		"/nodes/desktop/lxc/108": "/nodes/desktop/lxc",
		"/cluster/sdn/vnets/v0":  "/cluster/sdn/vnets",
		"/version":               "",
	} {
		if got := parentCollection(in); got != want {
			t.Errorf("parentCollection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompactJSON(t *testing.T) {
	out, err := compactJSON([]byte("{\n \"b\": 2,\n \"a\": 1\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"a":1,"b":2}` {
		t.Fatalf("compactJSON = %q", out)
	}
}
