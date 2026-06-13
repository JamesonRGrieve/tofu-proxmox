// SPDX-License-Identifier: AGPL-3.0-or-later

package proxmox

import "testing"

func TestParseUPID(t *testing.T) {
	valid := "UPID:desktop:0001A2B3:0C4D5E6F:65A1B2C3:vzcreate:108:root@pam:"
	u, err := ParseUPID(valid)
	if err != nil {
		t.Fatalf("ParseUPID(valid) error: %v", err)
	}
	if u.Node != "desktop" {
		t.Errorf("Node=%q, want desktop", u.Node)
	}
	if u.Type != "vzcreate" {
		t.Errorf("Type=%q, want vzcreate", u.Type)
	}
	if u.Raw != valid {
		t.Errorf("Raw not preserved")
	}

	bad := []string{
		"",
		"not-a-upid",
		"UPID:onlyfour:parts:here",
		"NOTUPID:desktop:1:2:3:vzcreate:108:root@pam:",
		"UPID::1:2:3:vzcreate:108:root@pam:", // empty node
	}
	for _, s := range bad {
		if _, err := ParseUPID(s); err == nil {
			t.Errorf("ParseUPID(%q) = nil error, want error", s)
		}
	}
}

func TestUPIDFromData(t *testing.T) {
	if _, ok := UPIDFromData([]byte(`"UPID:desktop:1:2:3:vzstart:108:root@pam:"`)); !ok {
		t.Error("JSON UPID string should parse")
	}
	if _, ok := UPIDFromData([]byte(`{"cores":4}`)); ok {
		t.Error("JSON object is not a UPID")
	}
	if _, ok := UPIDFromData([]byte(`"just a string"`)); ok {
		t.Error("non-UPID string should not parse")
	}
}

func TestTaskStatus(t *testing.T) {
	cases := []struct {
		st       TaskStatus
		wantDone bool
		wantOK   bool
	}{
		{TaskStatus{Status: "running"}, false, false},
		{TaskStatus{Status: "stopped", ExitStatus: "OK"}, true, true},
		{TaskStatus{Status: "stopped", ExitStatus: "command failed"}, true, false},
	}
	for _, tc := range cases {
		if tc.st.Done() != tc.wantDone {
			t.Errorf("%+v Done()=%v want %v", tc.st, tc.st.Done(), tc.wantDone)
		}
		if tc.st.OK() != tc.wantOK {
			t.Errorf("%+v OK()=%v want %v", tc.st, tc.st.OK(), tc.wantOK)
		}
	}
}
