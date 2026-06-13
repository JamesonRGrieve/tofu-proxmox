// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"fmt"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*objectDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*objectDataSource)(nil)
)

// NewObjectDataSource constructs the generic proxmox_object data source.
func NewObjectDataSource() datasource.DataSource { return &objectDataSource{} }

type objectDataSource struct {
	client *proxmox.Client
}

type objectDataModel struct {
	Path     types.String `tfsdk:"path"`
	Response types.String `tfsdk:"response"`
}

func (d *objectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object"
}

func (d *objectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Read any Proxmox resource by its `/api2/json` path (the `data` field is returned).",
		Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Resource path under `/api2/json` (leading slash optional), e.g. `/nodes`, `/nodes/desktop/lxc/108/config`.",
			},
			"response": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The JSON `data` value from the server response.",
			},
		},
	}
}

func (d *objectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*proxmox.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *proxmox.Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *objectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m objectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	raw, err := d.client.Get(normPath(m.Path.ValueString()))
	if err != nil {
		resp.Diagnostics.AddError("Proxmox read failed", err.Error())
		return
	}
	compact, err := compactJSON(raw)
	if err != nil {
		resp.Diagnostics.AddError("Proxmox read: invalid JSON from server", err.Error())
		return
	}
	m.Response = types.StringValue(compact)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
