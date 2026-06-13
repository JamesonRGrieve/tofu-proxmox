// SPDX-License-Identifier: AGPL-3.0-or-later

// Package provider implements the proxmox OpenTofu/Terraform provider — a native
// client for the Proxmox product family (PVE/PBS/PMG/PDM) /api2/json REST API.
// It is generic over the API surface: proxmox_object addresses any config path
// (manage-declared-only, import-to-0-diff) and proxmox_task issues any async
// operation (polling the returned UPID), giving 100% coverage without
// per-feature code. Each provider instance binds to one product endpoint;
// instantiate it per host with alias/for_each.
package provider

import (
	"context"
	"time"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = (*proxmoxProvider)(nil)

// New returns the provider factory for a given version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &proxmoxProvider{version: version} }
}

type proxmoxProvider struct {
	version string
}

type providerModel struct {
	Product        types.String `tfsdk:"product"`
	Host           types.String `tfsdk:"host"`
	Port           types.Int64  `tfsdk:"port"`
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	APITokenID     types.String `tfsdk:"api_token_id"`
	APITokenSecret types.String `tfsdk:"api_token_secret"`
	Insecure       types.Bool   `tfsdk:"insecure"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

func (p *proxmoxProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	// Single-token type name -> resources are `proxmox_object` / `proxmox_task`
	// (Terraform's prefix-before-first-underscore inference resolves the local
	// name); the source address is still jamesonrgrieve/proxmox.
	resp.TypeName = "proxmox"
	resp.Version = p.version
}

func (p *proxmoxProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Native provider for the Proxmox product family (PVE/PBS/PMG/PDM) via the shared " +
			"`/api2/json` REST API. Each instance binds to one product endpoint; instantiate per host with " +
			"`alias`/`for_each`. Prefer API-token auth (PMG supports tickets only).",
		Attributes: map[string]schema.Attribute{
			"product": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Proxmox product: `pve` (default), `pbs`, `pmg`, or `pdm`. Selects the default port, session-cookie name, and API-token scheme.",
			},
			"host": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Server address (host or host:port), no scheme.",
			},
			"port": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "API port. Defaults per product: PVE/PMG/PDM `8006`, PBS `8007`.",
			},
			"username": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "`user@realm` for ticket auth (required for PMG; alternative to an API token elsewhere).",
			},
			"password": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "Password for ticket auth.",
			},
			"api_token_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "`user@realm!tokenid` for API-token auth (preferred — stateless, no CSRF). Not supported on PMG.",
			},
			"api_token_secret": schema.StringAttribute{
				Optional:            true,
				Sensitive:           true,
				MarkdownDescription: "API token secret (UUID).",
			},
			"insecure": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Skip TLS verification (default true — Proxmox ships a self-signed cert).",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Per-request HTTP timeout in seconds (default 30).",
			},
		},
	}
}

func (p *proxmoxProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	product := proxmox.PVE
	if !cfg.Product.IsNull() && !cfg.Product.IsUnknown() && cfg.Product.ValueString() != "" {
		product = proxmox.Product(cfg.Product.ValueString())
	}
	if !product.Valid() {
		resp.Diagnostics.AddAttributeError(
			path.Root("product"),
			"Unknown product",
			"`product` must be one of pve, pbs, pmg, pdm; got "+string(product),
		)
		return
	}

	insecure := true
	if !cfg.Insecure.IsNull() && !cfg.Insecure.IsUnknown() {
		insecure = cfg.Insecure.ValueBool()
	}

	c, err := proxmox.NewClient(proxmox.Config{
		Product:     product,
		Host:        cfg.Host.ValueString(),
		Port:        int(cfg.Port.ValueInt64()),
		Username:    cfg.Username.ValueString(),
		Password:    cfg.Password.ValueString(),
		TokenID:     cfg.APITokenID.ValueString(),
		TokenSecret: cfg.APITokenSecret.ValueString(),
		Insecure:    insecure,
		Timeout:     time.Duration(cfg.TimeoutSeconds.ValueInt64()) * time.Second,
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid Proxmox provider configuration", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *proxmoxProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{NewObjectResource, NewTaskResource}
}

func (p *proxmoxProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{NewObjectDataSource, NewTaskDataSource}
}
