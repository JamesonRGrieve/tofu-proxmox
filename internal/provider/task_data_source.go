// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = (*taskDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*taskDataSource)(nil)
)

// NewTaskDataSource constructs the proxmox_task data source — reads a task's
// current status by UPID (a one-shot status GET, no polling).
func NewTaskDataSource() datasource.DataSource { return &taskDataSource{} }

type taskDataSource struct {
	client *proxmox.Client
}

type taskDataModel struct {
	UPID       types.String `tfsdk:"upid"`
	Status     types.String `tfsdk:"status"`
	ExitStatus types.String `tfsdk:"exit_status"`
	Running    types.Bool   `tfsdk:"running"`
}

func (d *taskDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task"
}

func (d *taskDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Read a Proxmox task's current status by UPID.",
		Attributes: map[string]schema.Attribute{
			"upid": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Task UPID (e.g. from `proxmox_task.x.upid`).",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`running` or `stopped`.",
			},
			"exit_status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`OK` on success, or the error string, once stopped.",
			},
			"running": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "True while the task is still running.",
			},
		},
	}
}

func (d *taskDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *taskDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m taskDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := proxmox.ParseUPID(m.UPID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid UPID", err.Error())
		return
	}
	raw, err := d.client.Get(fmt.Sprintf("/nodes/%s/tasks/%s/status", u.Node, url.PathEscape(u.Raw)))
	if err != nil {
		resp.Diagnostics.AddError("Proxmox task status read failed", err.Error())
		return
	}
	var st proxmox.TaskStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		resp.Diagnostics.AddError("Proxmox task status: invalid JSON", err.Error())
		return
	}
	m.Status = types.StringValue(st.Status)
	m.ExitStatus = types.StringValue(st.ExitStatus)
	m.Running = types.BoolValue(!st.Done())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
