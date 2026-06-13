// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource              = (*taskResource)(nil)
	_ resource.ResourceWithConfigure = (*taskResource)(nil)
)

// NewTaskResource constructs the imperative proxmox_task resource — the action
// analogue of proxmox_object. It issues any API call and, when the call returns
// a UPID (a background task), polls the task to completion.
func NewTaskResource() resource.Resource { return &taskResource{} }

type taskResource struct {
	client *proxmox.Client
}

type taskModel struct {
	ID             types.String `tfsdk:"id"`
	Method         types.String `tfsdk:"method"`
	Path           types.String `tfsdk:"path"`
	Params         types.String `tfsdk:"params"`
	Triggers       types.Map    `tfsdk:"triggers"`
	Await          types.Bool   `tfsdk:"await"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
	DestroyMethod  types.String `tfsdk:"destroy_method"`
	DestroyPath    types.String `tfsdk:"destroy_path"`
	DestroyParams  types.String `tfsdk:"destroy_params"`
	UPID           types.String `tfsdk:"upid"`
	Result         types.String `tfsdk:"result"`
}

func (r *taskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_task"
}

func (r *taskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Issue an arbitrary Proxmox API call — the imperative resource for **async** lifecycle " +
			"operations (create/clone/start/stop/migrate/destroy a VM or CT, backups). When the call returns a UPID, " +
			"the task is polled to completion and a non-OK result is a hard error. Re-invoked when `triggers` change.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "`<METHOD> <path>` identifier.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"method": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "HTTP method: `POST`, `PUT`, or `DELETE`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"path": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "API path under `/api2/json` (leading slash optional), e.g. `/nodes/desktop/lxc` or `/nodes/desktop/lxc/108/status/start`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"params": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "JSON object of request parameters, e.g. `{\"vmid\":108,\"hostname\":\"sglang\"}`. Defaults to none.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"triggers": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Arbitrary values that, when changed, re-invoke the call (like `null_resource` triggers).",
			},
			"await": schema.BoolAttribute{
				Optional:            true,
				MarkdownDescription: "Poll the returned UPID to completion (default true). When false, the call returns immediately.",
			},
			"timeout_seconds": schema.Int64Attribute{
				Optional:            true,
				MarkdownDescription: "Max seconds to await task completion (default 600).",
			},
			"destroy_method": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional inverse operation issued on destroy (e.g. `DELETE`). When unset, destroy is a no-op.",
			},
			"destroy_path": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "API path for the destroy operation (required when `destroy_method` is set).",
			},
			"destroy_params": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "JSON parameters for the destroy operation, e.g. `{\"purge\":1,\"force\":1}`.",
			},
			"upid": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The UPID returned by the most recent invocation (empty for synchronous calls).",
			},
			"result": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "JSON `data` from the most recent invocation.",
			},
		},
	}
}

func (r *taskResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// run issues one call and, if it returns a UPID and await is set, polls it. It
// holds writeMu for the whole operation so concurrent applies don't race on the
// same host (PVE locks guest config server-side).
func (r *taskResource) run(ctx context.Context, method, rawPath, paramsJSON string, await bool, timeoutSecs int64) (upid, result string, err error) {
	p := normPath(rawPath)
	var body []byte
	if s := strings.TrimSpace(paramsJSON); s != "" && s != "{}" {
		if !json.Valid([]byte(s)) {
			return "", "", fmt.Errorf("`params` must be valid JSON")
		}
		body = []byte(s)
	}

	r.client.LockWrites()
	defer r.client.UnlockWrites()

	var data []byte
	switch strings.ToUpper(method) {
	case http.MethodPost:
		data, err = r.client.Post(p, body)
	case http.MethodPut:
		data, err = r.client.Put(p, body)
	case http.MethodDelete:
		data, err = r.client.Delete(p)
	default:
		return "", "", fmt.Errorf("unsupported method %q (want POST, PUT, or DELETE)", method)
	}
	if err != nil {
		return "", "", err
	}
	result = string(data)

	u, ok := proxmox.UPIDFromData(data)
	if !ok {
		return "", result, nil // synchronous call
	}
	upid = u.Raw
	if !await {
		return upid, result, nil
	}
	to := time.Duration(timeoutSecs) * time.Second
	if to <= 0 {
		to = 600 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	st, werr := r.client.TaskWait(cctx, u, 0)
	if werr != nil {
		return upid, result, fmt.Errorf("waiting for task %s: %v\n%s", u.Raw, werr, r.client.TaskLogTail(u, 20))
	}
	if !st.OK() {
		return upid, result, fmt.Errorf("task %s failed: %s\n%s", u.Raw, st.ExitStatus, r.client.TaskLogTail(u, 20))
	}
	return upid, result, nil
}

func (r *taskResource) awaitFlag(m taskModel) bool {
	if m.Await.IsNull() || m.Await.IsUnknown() {
		return true
	}
	return m.Await.ValueBool()
}

func (r *taskResource) timeout(m taskModel) int64 {
	if m.TimeoutSeconds.IsNull() || m.TimeoutSeconds.IsUnknown() {
		return 600
	}
	return m.TimeoutSeconds.ValueInt64()
}

func (r *taskResource) apply(ctx context.Context, m *taskModel) error {
	upid, result, err := r.run(ctx, m.Method.ValueString(), m.Path.ValueString(), m.Params.ValueString(), r.awaitFlag(*m), r.timeout(*m))
	if err != nil {
		return err
	}
	m.UPID = types.StringValue(upid)
	m.Result = types.StringValue(result)
	m.ID = types.StringValue(strings.ToUpper(m.Method.ValueString()) + " " + normPath(m.Path.ValueString()))
	return nil
}

func (r *taskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan taskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Proxmox task failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read is a no-op refresh: an imperative action has no idempotent current value.
func (r *taskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state taskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *taskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan taskModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		resp.Diagnostics.AddError("Proxmox task failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete runs the optional inverse operation (destroy_*), else is a no-op.
func (r *taskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state taskModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DestroyMethod.IsNull() || state.DestroyMethod.ValueString() == "" {
		return
	}
	if state.DestroyPath.IsNull() || state.DestroyPath.ValueString() == "" {
		resp.Diagnostics.AddError("destroy_method requires destroy_path", "no destroy path provided")
		return
	}
	if _, _, err := r.run(ctx, state.DestroyMethod.ValueString(), state.DestroyPath.ValueString(), state.DestroyParams.ValueString(), true, r.timeout(state)); err != nil {
		resp.Diagnostics.AddError("Proxmox destroy task failed", err.Error())
	}
}
