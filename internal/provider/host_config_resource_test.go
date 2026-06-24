// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"reflect"
	"testing"
)

func TestBuildResolvConf(t *testing.T) {
	got := buildResolvConf([]string{"1.1.1.1", "8.8.8.8"}, "lab.local")
	want := "search lab.local\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	if got != want {
		t.Fatalf("buildResolvConf = %q, want %q", got, want)
	}
	if got := buildResolvConf([]string{"9.9.9.9"}, ""); got != "nameserver 9.9.9.9\n" {
		t.Fatalf("no-search resolv = %q", got)
	}
}

func TestBuildChronySources(t *testing.T) {
	got := buildChronySources([]string{"0.pool.ntp.org", "1.pool.ntp.org"})
	want := "server 0.pool.ntp.org iburst\nserver 1.pool.ntp.org iburst\n"
	if got != want {
		t.Fatalf("buildChronySources = %q, want %q", got, want)
	}
	if got := buildChronySources(nil); got != "" {
		t.Fatalf("empty chrony = %q", got)
	}
}

func TestBuildRsyslogConf(t *testing.T) {
	if got := buildRsyslogConf("10.0.0.1", "udp", ""); got != "# Managed by OpenTofu — do not edit manually\n*.* @10.0.0.1\n" {
		t.Fatalf("udp default-filter = %q", got)
	}
	if got := buildRsyslogConf("10.0.0.1", "tcp", "*.info"); got != "# Managed by OpenTofu — do not edit manually\n*.info @@10.0.0.1\n" {
		t.Fatalf("tcp custom-filter = %q", got)
	}
}

func TestBuildLogrotateConf(t *testing.T) {
	want := "# Managed by OpenTofu — do not edit manually\ndaily\nrotate 14\ncompress\n"
	if got := buildLogrotateConf(14); got != want {
		t.Fatalf("buildLogrotateConf = %q, want %q", got, want)
	}
}

func TestBuildPasswordAuthConf(t *testing.T) {
	if got := buildPasswordAuthConf(false); got != "# Managed by OpenTofu — do not edit manually\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n" {
		t.Fatalf("disabled = %q", got)
	}
	if got := buildPasswordAuthConf(true); got != "# Managed by OpenTofu — do not edit manually\nPasswordAuthentication yes\nKbdInteractiveAuthentication yes\n" {
		t.Fatalf("enabled = %q", got)
	}
}

func TestBuildAllowUsersConf(t *testing.T) {
	// connUser is appended so the provider can never lock itself out.
	got := buildAllowUsersConf([]string{"alice"}, []string{"admins"}, "root")
	want := "# Managed by OpenTofu — do not edit manually\nAllowUsers alice root\nAllowGroups admins\n"
	if got != want {
		t.Fatalf("with-conn-user = %q, want %q", got, want)
	}
	// connUser already present → not duplicated.
	if got := buildAllowUsersConf([]string{"root", "bob"}, nil, "root"); got != "# Managed by OpenTofu — do not edit manually\nAllowUsers root bob\n" {
		t.Fatalf("conn-user present = %q", got)
	}
	// Nothing to write.
	if got := buildAllowUsersConf(nil, nil, ""); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestBuildSNMPConf(t *testing.T) {
	v2 := buildSNMPConf("public", "admin@lab", "Rack 3", "10.0.0.2", nil)
	want := "# Managed by OpenTofu — do not edit manually\n" +
		"agentaddress udp:10.0.0.2:161\n" +
		"rocommunity public\n" +
		"sysContact admin@lab\n" +
		"sysLocation Rack 3\n" +
		"includeDir /etc/snmp/snmpd.conf.d\n"
	if v2 != want {
		t.Fatalf("v2c snmpd.conf = %q, want %q", v2, want)
	}

	v3 := buildSNMPConf("", "", "", "", &snmpv3Vals{
		user: "monitor", authProto: "SHA", authPass: "authpass123", privProto: "AES", privPass: "privpass123", secLevel: "authPriv",
	})
	wantV3 := "# Managed by OpenTofu — do not edit manually\n" +
		"agentaddress udp:161\n" +
		"createUser monitor SHA \"authpass123\" AES \"privpass123\"\n" +
		"rwuser monitor authPriv\n" +
		"includeDir /etc/snmp/snmpd.conf.d\n"
	if v3 != wantV3 {
		t.Fatalf("v3 snmpd.conf = %q, want %q", v3, wantV3)
	}
}

func TestBuildTrapConf(t *testing.T) {
	if got := buildTrapConf("10.0.0.9:162"); got != "# Managed by OpenTofu — do not edit manually\ntrap2sink 10.0.0.9:162\n" {
		t.Fatalf("buildTrapConf = %q", got)
	}
}

func TestParseReadOutput(t *testing.T) {
	out := []byte("hostname=pve-lab\ntimezone=UTC\ndns_servers=1.1.1.1 8.8.8.8\n" +
		"snmp_contact=a=b\nblankline\n\nssh_port=2222\r\n")
	m := parseReadOutput(out)
	if m["hostname"] != "pve-lab" || m["timezone"] != "UTC" {
		t.Fatalf("scalar parse: %#v", m)
	}
	if m["dns_servers"] != "1.1.1.1 8.8.8.8" {
		t.Fatalf("space value: %q", m["dns_servers"])
	}
	// Value containing '=' must survive (split on first '=' only).
	if m["snmp_contact"] != "a=b" {
		t.Fatalf("equals-in-value: %q", m["snmp_contact"])
	}
	// CR stripped; a line with no '=' ignored.
	if m["ssh_port"] != "2222" {
		t.Fatalf("cr strip: %q", m["ssh_port"])
	}
	if _, ok := m["blankline"]; ok {
		t.Fatalf("line without '=' should be ignored")
	}
}

func TestSplitFields(t *testing.T) {
	if got := splitFields(""); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("empty = %#v", got)
	}
	if got := splitFields("  a   b c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("fields = %#v", got)
	}
}

func TestOrDefault(t *testing.T) {
	if orDefault("", "SHA") != "SHA" {
		t.Fatal("empty should fall to default")
	}
	if orDefault("MD5", "SHA") != "MD5" {
		t.Fatal("set value should win")
	}
}

func TestHCQuote(t *testing.T) {
	if got := hcQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("hcQuote = %q", got)
	}
}
