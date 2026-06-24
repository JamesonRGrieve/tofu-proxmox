// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// proxmox_host_config owns a Proxmox node's Debian-OS settings that have NO
// /api2/json endpoint — hostname, timezone, /etc/resolv.conf, chrony NTP,
// remote rsyslog, logrotate retention, sshd_config.d drop-ins, and snmpd.conf
// (v2c + v3). They are applied over the provider's SSH transport (key/cert auth,
// see internal/proxmox/ssh.go), folding the 15 hv/_common scottwinkler/shell
// modules (the 60 sshpass scripts) into one provider resource. Singleton (one
// per node); every attribute is OPTIONAL and only declared (non-null) ones are
// managed — an unset attribute is neither read for drift nor written (subset
// semantics, mirroring opnsense_system_config).
var (
	_ resource.Resource                = (*hostConfigResource)(nil)
	_ resource.ResourceWithConfigure   = (*hostConfigResource)(nil)
	_ resource.ResourceWithImportState = (*hostConfigResource)(nil)
)

// NewHostConfigResource constructs the proxmox_host_config resource.
func NewHostConfigResource() resource.Resource { return &hostConfigResource{} }

type hostConfigResource struct {
	client *proxmox.Client
}

type hostConfigModel struct {
	ID                types.String `tfsdk:"id"`
	Hostname          types.String `tfsdk:"hostname"`
	Timezone          types.String `tfsdk:"timezone"`
	DNSServers        types.List   `tfsdk:"dns_servers"`
	DNSSearchDomain   types.String `tfsdk:"dns_search_domain"`
	NTPServers        types.List   `tfsdk:"ntp_servers"`
	SyslogServer      types.String `tfsdk:"syslog_server"`
	SyslogProtocol    types.String `tfsdk:"syslog_protocol"`
	SyslogFilter      types.String `tfsdk:"syslog_filter"`
	LogRetentionDays  types.Int64  `tfsdk:"log_retention_days"`
	SSHPort           types.Int64  `tfsdk:"ssh_port"`
	SSHPasswordAuth   types.Bool   `tfsdk:"ssh_password_auth"`
	SSHAllowUsers     types.List   `tfsdk:"ssh_allow_users"`
	SSHAllowGroups    types.List   `tfsdk:"ssh_allow_groups"`
	SNMPCommunity     types.String `tfsdk:"snmp_community"`
	SNMPContact       types.String `tfsdk:"snmp_contact"`
	SNMPLocation      types.String `tfsdk:"snmp_location"`
	SNMPListenAddress types.String `tfsdk:"snmp_listen_address"`
	SNMPTrapTarget    types.String `tfsdk:"snmp_trap_target"`
	SNMPv3            types.Object `tfsdk:"snmpv3"`
}

type snmpv3Model struct {
	Username       types.String `tfsdk:"username"`
	AuthProtocol   types.String `tfsdk:"auth_protocol"`
	AuthPassphrase types.String `tfsdk:"auth_passphrase"`
	PrivProtocol   types.String `tfsdk:"priv_protocol"`
	PrivPassphrase types.String `tfsdk:"priv_passphrase"`
	SecurityLevel  types.String `tfsdk:"security_level"`
}

func (r *hostConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_host_config"
}

func (r *hostConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Proxmox node's Debian-OS settings that have no `/api2/json` endpoint (hostname, " +
			"timezone, DNS, chrony NTP, remote syslog, logrotate, sshd_config.d drop-ins, snmpd.conf v2c+v3), " +
			"applied over the provider's SSH transport. Singleton per node; every attribute is optional and only " +
			"declared attributes are managed (an unset attribute is left untouched). Requires the provider's SSH " +
			"transport (`ssh_host` + `ssh_key_file` or `ssh_key_pem`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"hostname": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "System hostname (`hostnamectl set-hostname`).",
			},
			"timezone": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "IANA timezone (`timedatectl set-timezone`).",
			},
			"dns_servers": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Upstream DNS servers, in order (`/etc/resolv.conf` `nameserver` lines).",
			},
			"dns_search_domain": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "DNS search domain (`/etc/resolv.conf` `search` line). When set without `dns_servers`, existing nameservers are preserved.",
			},
			"ntp_servers": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Chrony time sources (`/etc/chrony/sources.d/tofu-ntp.sources`; reloaded via `chronyc reload sources`).",
			},
			"syslog_server": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Remote rsyslog target (`/etc/rsyslog.d/50-remote.conf`). Empty leaves it unmanaged.",
			},
			"syslog_protocol": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Syslog transport: `udp` (rsyslog `@`) or `tcp` (`@@`). Default `udp`.",
			},
			"syslog_filter": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "rsyslog selector for the remote forward (e.g. `*.info`). Default `*.*` (all).",
			},
			"log_retention_days": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Rotated-log retention count (`/etc/logrotate.d/tofu-retention`: `daily`/`rotate N`/`compress`).",
			},
			"ssh_port": schema.Int64Attribute{
				Optional: true,
				MarkdownDescription: "sshd listen port (`Port` in `/etc/ssh/sshd_config`; restarts sshd). NOTE: after changing this, " +
					"update the provider's `ssh_port` so subsequent connections use the new port.",
			},
			"ssh_password_auth": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "sshd `PasswordAuthentication` + `KbdInteractiveAuthentication` (drop-in " +
					"`/etc/ssh/sshd_config.d/60-tofu-password-auth.conf`; reloads sshd). Consolidates the former ssh_password_auth + ssh_hardening modules.",
			},
			"ssh_allow_users": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "sshd `AllowUsers` (drop-in `/etc/ssh/sshd_config.d/10-allow-users.conf`; reloads sshd). " +
					"The provider's own SSH user is always appended so a bad list can never lock the transport out. Apply-only (not reconciled on read).",
			},
			"ssh_allow_groups": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "sshd `AllowGroups` (same drop-in as `ssh_allow_users`). Apply-only.",
			},
			"snmp_community": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "SNMP v2c read-only community (`rocommunity` in `/etc/snmp/snmpd.conf`). Installs snmpd if absent.",
			},
			"snmp_contact": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "snmpd `sysContact`.",
			},
			"snmp_location": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "snmpd `sysLocation`.",
			},
			"snmp_listen_address": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "snmpd listen IP (`agentaddress udp:<ip>:161`). Empty = all interfaces.",
			},
			"snmp_trap_target": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "snmpd `trap2sink` target (drop-in `/etc/snmp/snmpd.conf.d/trap-target.conf`).",
			},
			"snmpv3": schema.SingleNestedAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "SNMPv3 user (`createUser`/`rwuser` in `/etc/snmp/snmpd.conf`). Apply-only: snmpd consumes the " +
					"`createUser` passphrases on start, so they cannot be read back and are never reconciled (state holds the declared block).",
				Attributes: map[string]schema.Attribute{
					"username":        schema.StringAttribute{Required: true, MarkdownDescription: "SNMPv3 user name."},
					"auth_protocol":   schema.StringAttribute{Optional: true, MarkdownDescription: "Auth protocol (default SHA)."},
					"auth_passphrase": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Auth passphrase."},
					"priv_protocol":   schema.StringAttribute{Optional: true, MarkdownDescription: "Privacy protocol (default AES)."},
					"priv_passphrase": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "Privacy passphrase."},
					"security_level":  schema.StringAttribute{Optional: true, MarkdownDescription: "Security level (default authPriv)."},
				},
			},
		},
	}
}

func (r *hostConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*proxmox.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("expected *proxmox.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *hostConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m hostConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, m, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	}
	m.ID = types.StringValue("host")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *hostConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m hostConfigModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, m, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	}
	m.ID = types.StringValue("host")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// Delete is a no-op: node settings persist; we simply stop managing them
// (consistent with opnsense_system_config's singleton no-op delete).
func (r *hostConfigResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

func (r *hostConfigResource) ImportState(ctx context.Context, _ resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), "host")...)
}

func (r *hostConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m hostConfigModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil || r.client.SSH == nil {
		return
	}
	out, err := r.client.SSH.Run("/usr/bin/env bash -s", []byte(buildReadScript()))
	if err != nil {
		resp.Diagnostics.AddError("proxmox host_config read failed", err.Error())
		return
	}
	cur := parseReadOutput(out)

	// Subset refresh: only managed (non-null) attributes are reconciled; unset
	// attributes stay null so they are never tracked for drift. Apply-only fields
	// (ssh_allow_users/groups, snmpv3) are intentionally not reconciled.
	if !m.Hostname.IsNull() {
		m.Hostname = types.StringValue(cur["hostname"])
	}
	if !m.Timezone.IsNull() {
		m.Timezone = types.StringValue(cur["timezone"])
	}
	if !m.DNSServers.IsNull() {
		lv, d := types.ListValueFrom(ctx, types.StringType, splitFields(cur["dns_servers"]))
		resp.Diagnostics.Append(d...)
		m.DNSServers = lv
	}
	if !m.DNSSearchDomain.IsNull() {
		m.DNSSearchDomain = types.StringValue(cur["dns_search"])
	}
	if !m.NTPServers.IsNull() {
		lv, d := types.ListValueFrom(ctx, types.StringType, splitFields(cur["ntp_servers"]))
		resp.Diagnostics.Append(d...)
		m.NTPServers = lv
	}
	if !m.SyslogServer.IsNull() {
		m.SyslogServer = types.StringValue(cur["syslog_server"])
	}
	if !m.SyslogProtocol.IsNull() {
		m.SyslogProtocol = types.StringValue(cur["syslog_protocol"])
	}
	if !m.LogRetentionDays.IsNull() {
		if n, err := strconv.ParseInt(cur["log_retention"], 10, 64); err == nil {
			m.LogRetentionDays = types.Int64Value(n)
		}
	}
	if !m.SSHPort.IsNull() {
		if n, err := strconv.ParseInt(cur["ssh_port"], 10, 64); err == nil {
			m.SSHPort = types.Int64Value(n)
		}
	}
	if !m.SSHPasswordAuth.IsNull() {
		m.SSHPasswordAuth = types.BoolValue(strings.EqualFold(cur["ssh_password_auth"], "yes"))
	}
	if !m.SNMPCommunity.IsNull() {
		m.SNMPCommunity = types.StringValue(cur["snmp_community"])
	}
	if !m.SNMPContact.IsNull() {
		m.SNMPContact = types.StringValue(cur["snmp_contact"])
	}
	if !m.SNMPLocation.IsNull() {
		m.SNMPLocation = types.StringValue(cur["snmp_location"])
	}
	if !m.SNMPListenAddress.IsNull() {
		m.SNMPListenAddress = types.StringValue(cur["snmp_listen"])
	}
	if !m.SNMPTrapTarget.IsNull() {
		m.SNMPTrapTarget = types.StringValue(cur["snmp_trap"])
	}
	m.ID = types.StringValue("host")
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// apply runs the SSH commands for the declared attributes. Only declared
// (non-null) attributes are acted on, so an unset attribute is left untouched.
func (r *hostConfigResource) apply(ctx context.Context, m hostConfigModel, diags *diag.Diagnostics) {
	if r.client == nil || r.client.SSH == nil {
		diags.AddError("proxmox SSH transport not configured",
			"proxmox_host_config requires the provider's ssh_host + ssh_key_file or ssh_key_pem.")
		return
	}
	ssh := r.client.SSH
	run := func(what, cmd string, stdin []byte) bool {
		if _, err := ssh.Run(cmd, stdin); err != nil {
			diags.AddError("proxmox host_config "+what+" failed", err.Error())
			return false
		}
		return true
	}

	if !m.Hostname.IsNull() {
		if !run("hostname", "hostnamectl set-hostname "+hcQuote(m.Hostname.ValueString()), nil) {
			return
		}
	}
	if !m.Timezone.IsNull() {
		if !run("timezone", "timedatectl set-timezone "+hcQuote(m.Timezone.ValueString()), nil) {
			return
		}
	}
	if !m.DNSServers.IsNull() || !m.DNSSearchDomain.IsNull() {
		servers := hcList(ctx, m.DNSServers)
		search := m.DNSSearchDomain.ValueString()
		if m.DNSServers.IsNull() {
			// Only the search domain is managed — preserve existing nameservers.
			cmd := fmt.Sprintf("EXISTING=$(grep '^nameserver ' /etc/resolv.conf 2>/dev/null || true); "+
				"{ printf 'search %%s\\n' %s; printf '%%s\\n' \"$EXISTING\"; } > /etc/resolv.conf", hcQuote(search))
			if !run("dns", cmd, nil) {
				return
			}
		} else if !run("dns", "cat > /etc/resolv.conf", []byte(buildResolvConf(servers, search))) {
			return
		}
	}
	if !m.NTPServers.IsNull() {
		content := buildChronySources(hcList(ctx, m.NTPServers))
		cmd := "mkdir -p /etc/chrony/sources.d && cat > /etc/chrony/sources.d/tofu-ntp.sources && " +
			"{ chronyc reload sources >/dev/null 2>&1 || systemctl restart chronyd >/dev/null 2>&1 || systemctl restart chrony >/dev/null 2>&1 || true; }"
		if !run("ntp", cmd, []byte(content)) {
			return
		}
	}
	if !m.SyslogServer.IsNull() && m.SyslogServer.ValueString() != "" {
		content := buildRsyslogConf(m.SyslogServer.ValueString(), m.SyslogProtocol.ValueString(), m.SyslogFilter.ValueString())
		cmd := "mkdir -p /etc/rsyslog.d && cat > /etc/rsyslog.d/50-remote.conf && { systemctl restart rsyslog >/dev/null 2>&1 || true; }"
		if !run("syslog", cmd, []byte(content)) {
			return
		}
	}
	if !m.LogRetentionDays.IsNull() {
		content := buildLogrotateConf(m.LogRetentionDays.ValueInt64())
		if !run("logrotate", "cat > /etc/logrotate.d/tofu-retention", []byte(content)) {
			return
		}
	}
	if !m.SSHPort.IsNull() {
		port := m.SSHPort.ValueInt64()
		cmd := fmt.Sprintf("sed -i -E 's/^#?Port .*/Port %d/' /etc/ssh/sshd_config; "+
			"grep -qE '^Port ' /etc/ssh/sshd_config || echo 'Port %d' >> /etc/ssh/sshd_config; "+
			"{ systemctl restart sshd >/dev/null 2>&1 || systemctl restart ssh >/dev/null 2>&1 || true; }", port, port)
		if !run("ssh_port", cmd, nil) {
			return
		}
	}
	if !m.SSHPasswordAuth.IsNull() {
		content := buildPasswordAuthConf(m.SSHPasswordAuth.ValueBool())
		// Remove the legacy shell-module drop-in: it sorts before this file and
		// sshd's first-match-wins would otherwise override PasswordAuthentication.
		cmd := "mkdir -p /etc/ssh/sshd_config.d && rm -f /etc/ssh/sshd_config.d/50-tofu-hardening.conf && " +
			"cat > /etc/ssh/sshd_config.d/60-tofu-password-auth.conf && " +
			"{ systemctl reload sshd >/dev/null 2>&1 || systemctl reload ssh >/dev/null 2>&1 || true; }"
		if !run("ssh_password_auth", cmd, []byte(content)) {
			return
		}
	}
	if !m.SSHAllowUsers.IsNull() || !m.SSHAllowGroups.IsNull() {
		content := buildAllowUsersConf(hcList(ctx, m.SSHAllowUsers), hcList(ctx, m.SSHAllowGroups), ssh.User())
		if content != "" {
			cmd := "mkdir -p /etc/ssh/sshd_config.d && cat > /etc/ssh/sshd_config.d/10-allow-users.conf && " +
				"{ systemctl reload sshd >/dev/null 2>&1 || systemctl reload ssh >/dev/null 2>&1 || true; }"
			if !run("ssh_allow_users", cmd, []byte(content)) {
				return
			}
		}
	}

	// SNMP — owns /etc/snmp/snmpd.conf as one coherent unit (the 5 former snmp*
	// modules each fought over this file differently). Built from whatever SNMP
	// attributes are declared.
	v3 := r.snmpv3Values(ctx, m, diags)
	if diags.HasError() {
		return
	}
	manageSNMP := !m.SNMPCommunity.IsNull() || !m.SNMPContact.IsNull() || !m.SNMPLocation.IsNull() ||
		!m.SNMPListenAddress.IsNull() || v3 != nil
	if manageSNMP {
		content := buildSNMPConf(
			m.SNMPCommunity.ValueString(), m.SNMPContact.ValueString(), m.SNMPLocation.ValueString(),
			m.SNMPListenAddress.ValueString(), v3,
		)
		install := "dpkg -l snmpd 2>/dev/null | grep -q '^ii' || " +
			"{ DEBIAN_FRONTEND=noninteractive apt-get update -qq || true; DEBIAN_FRONTEND=noninteractive apt-get install -y -qq snmpd >/dev/null 2>&1; }"
		if !run("snmp install", install, nil) {
			return
		}
		if v3 != nil {
			// createUser is processed on startup; stop first and clear the persistent
			// usmUser so the user is re-created from the new passphrases.
			pre := "systemctl stop snmpd >/dev/null 2>&1 || true; sed -i '/^usmUser/d' /var/lib/snmp/snmpd.conf 2>/dev/null || true"
			if !run("snmp v3 pre", pre, nil) {
				return
			}
			if !run("snmp config", "cat > /etc/snmp/snmpd.conf", []byte(content)) {
				return
			}
			if !run("snmp v3 start", "systemctl enable snmpd >/dev/null 2>&1 || true; systemctl start snmpd", nil) {
				return
			}
		} else {
			if !run("snmp config", "cat > /etc/snmp/snmpd.conf", []byte(content)) {
				return
			}
			if !run("snmp restart", "systemctl enable snmpd >/dev/null 2>&1 || true; systemctl restart snmpd", nil) {
				return
			}
		}
	}
	if !m.SNMPTrapTarget.IsNull() && m.SNMPTrapTarget.ValueString() != "" {
		content := buildTrapConf(m.SNMPTrapTarget.ValueString())
		cmd := "mkdir -p /etc/snmp/snmpd.conf.d && " +
			"{ grep -qxF 'includeDir /etc/snmp/snmpd.conf.d' /etc/snmp/snmpd.conf 2>/dev/null || echo 'includeDir /etc/snmp/snmpd.conf.d' >> /etc/snmp/snmpd.conf; } && " +
			"cat > /etc/snmp/snmpd.conf.d/trap-target.conf && { systemctl restart snmpd >/dev/null 2>&1 || systemctl start snmpd >/dev/null 2>&1 || true; }"
		if !run("snmp trap", cmd, []byte(content)) {
			return
		}
	}
}

// snmpv3Values extracts the declared snmpv3 block (nil when unset), applying the
// net-snmp defaults for the optional protocol/level fields.
func (r *hostConfigResource) snmpv3Values(ctx context.Context, m hostConfigModel, diags *diag.Diagnostics) *snmpv3Vals {
	if m.SNMPv3.IsNull() || m.SNMPv3.IsUnknown() {
		return nil
	}
	var o snmpv3Model
	if d := m.SNMPv3.As(ctx, &o, basetypes.ObjectAsOptions{}); d.HasError() {
		diags.Append(d...)
		return nil
	}
	return &snmpv3Vals{
		user:      o.Username.ValueString(),
		authProto: orDefault(o.AuthProtocol.ValueString(), "SHA"),
		authPass:  o.AuthPassphrase.ValueString(),
		privProto: orDefault(o.PrivProtocol.ValueString(), "AES"),
		privPass:  o.PrivPassphrase.ValueString(),
		secLevel:  orDefault(o.SecurityLevel.ValueString(), "authPriv"),
	}
}

// ── pure builders (unit-tested; no SSH) ──────────────────────────────────────

type snmpv3Vals struct {
	user, authProto, authPass, privProto, privPass, secLevel string
}

func buildResolvConf(servers []string, search string) string {
	var b strings.Builder
	if strings.TrimSpace(search) != "" {
		fmt.Fprintf(&b, "search %s\n", search)
	}
	for _, s := range servers {
		fmt.Fprintf(&b, "nameserver %s\n", s)
	}
	return b.String()
}

func buildChronySources(servers []string) string {
	var b strings.Builder
	for _, s := range servers {
		fmt.Fprintf(&b, "server %s iburst\n", s)
	}
	return b.String()
}

func buildRsyslogConf(server, protocol, filter string) string {
	prefix := "@"
	if strings.EqualFold(protocol, "tcp") {
		prefix = "@@"
	}
	sel := strings.TrimSpace(filter)
	if sel == "" {
		sel = "*.*"
	}
	return fmt.Sprintf("# Managed by OpenTofu — do not edit manually\n%s %s%s\n", sel, prefix, server)
}

func buildLogrotateConf(days int64) string {
	return fmt.Sprintf("# Managed by OpenTofu — do not edit manually\ndaily\nrotate %d\ncompress\n", days)
}

func buildPasswordAuthConf(enabled bool) string {
	v := "no"
	if enabled {
		v = "yes"
	}
	return fmt.Sprintf("# Managed by OpenTofu — do not edit manually\nPasswordAuthentication %s\nKbdInteractiveAuthentication %s\n", v, v)
}

// buildAllowUsersConf renders the sshd AllowUsers/AllowGroups drop-in, always
// including connUser in AllowUsers so the provider can never lock its own SSH
// transport out. Empty when there is nothing to write.
func buildAllowUsersConf(users, groups []string, connUser string) string {
	all := append([]string{}, users...)
	if connUser != "" {
		present := false
		for _, u := range all {
			if u == connUser {
				present = true
				break
			}
		}
		if !present {
			all = append(all, connUser)
		}
	}
	var lines []string
	if len(all) > 0 {
		lines = append(lines, "AllowUsers "+strings.Join(all, " "))
	}
	if len(groups) > 0 {
		lines = append(lines, "AllowGroups "+strings.Join(groups, " "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "# Managed by OpenTofu — do not edit manually\n" + strings.Join(lines, "\n") + "\n"
}

// buildSNMPConf renders /etc/snmp/snmpd.conf from the declared SNMP attributes.
// An includeDir line keeps the trap drop-in active even when this resource owns
// the main file.
func buildSNMPConf(community, contact, location, listenIP string, v3 *snmpv3Vals) string {
	var b strings.Builder
	b.WriteString("# Managed by OpenTofu — do not edit manually\n")
	if listenIP != "" {
		fmt.Fprintf(&b, "agentaddress udp:%s:161\n", listenIP)
	} else {
		b.WriteString("agentaddress udp:161\n")
	}
	if v3 != nil {
		fmt.Fprintf(&b, "createUser %s %s %q %s %q\n", v3.user, v3.authProto, v3.authPass, v3.privProto, v3.privPass)
		fmt.Fprintf(&b, "rwuser %s %s\n", v3.user, v3.secLevel)
	}
	if community != "" {
		fmt.Fprintf(&b, "rocommunity %s\n", community)
	}
	if contact != "" {
		fmt.Fprintf(&b, "sysContact %s\n", contact)
	}
	if location != "" {
		fmt.Fprintf(&b, "sysLocation %s\n", location)
	}
	b.WriteString("includeDir /etc/snmp/snmpd.conf.d\n")
	return b.String()
}

func buildTrapConf(target string) string {
	return fmt.Sprintf("# Managed by OpenTofu — do not edit manually\ntrap2sink %s\n", target)
}

// buildReadScript emits one `key=value` line per managed setting (split on the
// first '=' in Go, so values may contain '='). Keeps reads to one SSH round-trip.
func buildReadScript() string {
	return `set +e
emit() { printf '%s=%s\n' "$1" "$2"; }
emit hostname "$(hostname 2>/dev/null)"
emit timezone "$(timedatectl show -p Timezone --value 2>/dev/null)"
emit dns_servers "$(grep '^nameserver ' /etc/resolv.conf 2>/dev/null | awk '{printf "%s ",$2}' | sed 's/ $//')"
emit dns_search "$(grep '^search ' /etc/resolv.conf 2>/dev/null | awk '{print $2}' | head -1)"
emit ntp_servers "$(grep '^server ' /etc/chrony/sources.d/tofu-ntp.sources 2>/dev/null | awk '{printf "%s ",$2}' | sed 's/ $//')"
RS="$(grep -v '^#' /etc/rsyslog.d/50-remote.conf 2>/dev/null | grep -v '^[[:space:]]*$' | head -1)"
emit syslog_server "$(printf '%s' "$RS" | grep -oE '@@?[^ ;]+' | head -1 | sed 's/^@*//')"
if printf '%s' "$RS" | grep -q '@@'; then emit syslog_protocol tcp; else if [ -n "$RS" ]; then emit syslog_protocol udp; else emit syslog_protocol ""; fi; fi
emit log_retention "$(grep -oE '^[[:space:]]*rotate[[:space:]]+[0-9]+' /etc/logrotate.d/tofu-retention 2>/dev/null | grep -oE '[0-9]+' | head -1)"
emit ssh_port "$(grep -E '^Port ' /etc/ssh/sshd_config 2>/dev/null | awk '{print $2}' | head -1)"
emit ssh_password_auth "$(grep -iE '^PasswordAuthentication' /etc/ssh/sshd_config.d/60-tofu-password-auth.conf 2>/dev/null | awk '{print $2}' | head -1)"
SC="$(cat /etc/snmp/snmpd.conf 2>/dev/null)"
emit snmp_community "$(printf '%s\n' "$SC" | grep -E '^rocommunity ' | awk '{print $2}' | head -1)"
emit snmp_contact "$(printf '%s\n' "$SC" | sed -n 's/^sysContact[[:space:]]\{1,\}//p' | head -1)"
emit snmp_location "$(printf '%s\n' "$SC" | sed -n 's/^sysLocation[[:space:]]\{1,\}//p' | head -1)"
emit snmp_listen "$(printf '%s\n' "$SC" | grep -iE '^agentaddress' | grep -oP 'udp:\K[0-9.]+(?=:161)' | head -1)"
emit snmp_trap "$(grep -oE '^trap2sink[[:space:]]+[^[:space:]]+' /etc/snmp/snmpd.conf.d/trap-target.conf 2>/dev/null | awk '{print $2}' | head -1)"
`
}

// parseReadOutput turns the read script's key=value lines into a map (first '='
// per line is the separator; missing keys read as "").
func parseReadOutput(out []byte) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		m[strings.TrimSpace(line[:i])] = strings.TrimRight(line[i+1:], "\r")
	}
	return m
}

// ── helpers ──────────────────────────────────────────────────────────────────

func splitFields(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	return strings.Fields(s)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// hcQuote single-quotes a value for safe use in a remote shell command.
func hcQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func hcList(ctx context.Context, l types.List) []string {
	var out []string
	if l.IsNull() || l.IsUnknown() {
		return out
	}
	_ = l.ElementsAs(ctx, &out, false)
	return out
}
