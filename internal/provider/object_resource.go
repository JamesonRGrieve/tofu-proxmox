// SPDX-License-Identifier: AGPL-3.0-or-later

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/proxmox"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = (*objectResource)(nil)
	_ resource.ResourceWithConfigure   = (*objectResource)(nil)
	_ resource.ResourceWithImportState = (*objectResource)(nil)
)

// NewObjectResource constructs the generic proxmox_object resource.
func NewObjectResource() resource.Resource { return &objectResource{} }

type objectResource struct {
	client *proxmox.Client
}

// objectModel is the state/plan shape for proxmox_object.
type objectModel struct {
	ID           types.String `tfsdk:"id"`
	Path         types.String `tfsdk:"path"`
	CreatePath   types.String `tfsdk:"create_path"`
	DeleteMethod types.String `tfsdk:"delete_method"`
	DeleteBody   types.String `tfsdk:"delete_body"`
	Body         types.String `tfsdk:"body"`
}

func (r *objectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_object"
}

func (r *objectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A generic Proxmox resource addressed by its `/api2/json` path, for **synchronous** " +
			"config endpoints (e.g. `/nodes/{node}/lxc/{vmid}/config`, `/access/users/{id}`, storage, SDN). " +
			"`body` declares only the keys this resource manages; device-returned keys outside `body` are ignored " +
			"for drift, so a subset declaration imports to 0-diff and never clobbers unmanaged fields. " +
			"For **async** lifecycle ops that return a UPID (create/start/migrate/destroy a guest), use `proxmox_task`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Resource id — equal to `path`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"path": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Addressed resource path under `/api2/json` (leading slash optional), used for " +
					"GET/PUT/DELETE. E.g. `/nodes/desktop/lxc/108/config`, `/access/users/svc@pve`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"create_path": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Collection path to POST to on create (e.g. `/cluster/sdn/vnets` while `path` is " +
					"`/cluster/sdn/vnets/vnet0`). When unset, create is an idempotent PUT to `path`. Carry it in the " +
					"import id (`<path>|<create_path>`) so an imported resource lands at 0-diff.",
			},
			"delete_method": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "How to destroy: `DELETE` (default), `PUT` (send `delete_body` to `path`), or " +
					"`NONE` (no-op — e.g. when a `proxmox_task` owns the guest lifecycle and this resource only manages " +
					"its config). NOTE: PVE removes config keys via a `delete=` param, not DELETE-on-subpath — to unset " +
					"keys use `delete_method = \"PUT\"` with `delete_body = {\"delete\":\"hookscript,mp0\"}`.",
			},
			"delete_body": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "JSON body PUT to `path` on destroy when `delete_method = \"PUT\"`. Import id field 4.",
			},
			"body": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "JSON object of the declared (managed) attributes. State holds the full device " +
					"object; drift is detected only on these keys.",
				PlanModifiers: []planmodifier.String{subsetSuppress{}},
			},
		},
	}
}

func (r *objectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// normPath ensures a leading slash.
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// parentCollection returns the collection path for an item path by dropping the
// last segment. Returns "" for a top-level singleton (no parent).
func parentCollection(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

func (r *objectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := []byte(m.Body.ValueString())
	if !json.Valid(body) {
		resp.Diagnostics.AddError("Invalid body", "`body` must be valid JSON")
		return
	}
	r.client.LockWrites()
	defer r.client.UnlockWrites()
	var err error
	if !m.CreatePath.IsNull() && m.CreatePath.ValueString() != "" {
		_, err = r.client.Post(normPath(m.CreatePath.ValueString()), body)
	} else {
		// Idempotent PUT to the item path; fall back to POSTing the parent
		// collection if the item path 404s (covers POST-create collections
		// without an explicit create_path).
		p := normPath(m.Path.ValueString())
		_, err = r.client.Put(p, body)
		if err != nil && proxmox.NotFound(err) {
			if parent := parentCollection(p); parent != "" {
				_, err = r.client.Post(parent, body)
			}
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Proxmox create failed", err.Error())
		return
	}
	m.ID = m.Path
	// Store the declared body verbatim; the next refresh (Read) replaces it with
	// the full device object.
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *objectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	raw, err := r.client.Get(normPath(m.Path.ValueString()))
	if err != nil {
		if proxmox.NotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Proxmox read failed", err.Error())
		return
	}
	compact, err := compactJSON(raw)
	if err != nil {
		resp.Diagnostics.AddError("Proxmox read: invalid JSON from server", err.Error())
		return
	}
	m.Body = types.StringValue(compact)
	m.ID = m.Path
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *objectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m objectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := []byte(m.Body.ValueString())
	if !json.Valid(body) {
		resp.Diagnostics.AddError("Invalid body", "`body` must be valid JSON")
		return
	}
	r.client.LockWrites()
	defer r.client.UnlockWrites()
	if _, err := r.client.Put(normPath(m.Path.ValueString()), body); err != nil {
		resp.Diagnostics.AddError("Proxmox update failed", err.Error())
		return
	}
	m.ID = m.Path
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *objectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m objectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	method := "DELETE"
	if !m.DeleteMethod.IsNull() && m.DeleteMethod.ValueString() != "" {
		method = strings.ToUpper(m.DeleteMethod.ValueString())
	}
	r.client.LockWrites()
	defer r.client.UnlockWrites()
	var err error
	switch method {
	case "NONE":
		// Config-only resource whose guest lifecycle is owned elsewhere; drop state.
	case "PUT":
		if m.DeleteBody.IsNull() {
			resp.Diagnostics.AddError("delete_method=PUT requires delete_body", "no reset/delete body provided")
			return
		}
		_, err = r.client.Put(normPath(m.Path.ValueString()), []byte(m.DeleteBody.ValueString()))
	default: // DELETE
		_, err = r.client.Delete(normPath(m.Path.ValueString()))
		if err != nil && proxmox.NotFound(err) {
			err = nil // already gone
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Proxmox delete failed", err.Error())
	}
}

func (r *objectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import id is a pipe-delimited tuple matching config's operational hints
	// (→ 0-diff): <path>[|<create_path>[|<delete_method>[|<delete_body>]]].
	// Empty fields → null. Body is populated on the following Read.
	parts := strings.Split(req.ID, "|")
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	setOpt := func(p string, i int) {
		if i < len(parts) && parts[i] != "" {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(p), parts[i])...)
		} else {
			resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(p), types.StringNull())...)
		}
	}
	setOpt("create_path", 1)
	setOpt("delete_method", 2)
	setOpt("delete_body", 3)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("body"), "{}")...)
}

// ---------------------------------------------------------------------------
// subset plan modifier — suppress diff when every declared key already matches
// the full device object held in prior state. Lets a subset `body` import/
// refresh to 0-diff without clobbering unmanaged device fields.
// ---------------------------------------------------------------------------

type subsetSuppress struct{}

func (subsetSuppress) Description(context.Context) string {
	return "Suppress diff when all declared JSON keys already match the device object in state."
}
func (subsetSuppress) MarkdownDescription(context.Context) string {
	return (subsetSuppress{}).Description(nil)
}

func (subsetSuppress) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if subsetMatches(req.StateValue.ValueString(), req.ConfigValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}

// subsetMatches reports whether every top-level key in cfg is present in prior
// with a structurally-equal value (cfg is a value-subset of prior). Invalid JSON
// on either side returns false so the caller falls back to a normal diff.
func subsetMatches(prior, cfg string) bool {
	var p, c map[string]json.RawMessage
	if json.Unmarshal([]byte(prior), &p) != nil {
		return false
	}
	if json.Unmarshal([]byte(cfg), &c) != nil {
		return false
	}
	for k, cv := range c {
		pv, ok := p[k]
		if !ok || !jsonEqual(cv, pv) {
			return false
		}
	}
	return true
}

// jsonEqual compares two raw JSON values structurally (order-insensitive).
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// compactJSON re-serializes raw JSON in compact, key-sorted form.
func compactJSON(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
