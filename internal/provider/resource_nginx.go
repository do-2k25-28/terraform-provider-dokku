package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource                = &NginxResource{}
	_ resource.ResourceWithConfigure   = &NginxResource{}
	_ resource.ResourceWithImportState = &NginxResource{}
)

func NewNginxResource() resource.Resource { return &NginxResource{} }

// NginxResource controls whether Dokku's global nginx server is running
// (`dokku nginx:start` / `dokku nginx:stop`). Dokku exposes no status query
// for this, so state is trusted rather than verified on Read.
type NginxResource struct {
	client *dokku.Client
}

type NginxResourceModel struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	ID      types.String `tfsdk:"id"`
}

func (r *NginxResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_nginx"
}

func (r *NginxResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Controls whether the global Dokku nginx server is running (`dokku nginx:start` / `dokku nginx:stop`).",
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether the nginx server should be running. `true` runs `dokku nginx:start`, `false` runs `dokku nginx:stop`.",
			},
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *NginxResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dokku.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected resource configure type", "Expected *dokku.Client")
		return
	}
	r.client = client
}

func (r *NginxResource) apply(enabled bool) error {
	if enabled {
		_, err := r.client.RunChecked("nginx:start")
		return err
	}
	_, err := r.client.RunChecked("nginx:stop")
	return err
}

func (r *NginxResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data NginxResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error setting nginx server state", err.Error())
		return
	}

	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *NginxResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data NginxResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Dokku has no command to query whether the global nginx server is
	// currently running, so the last-applied state is trusted as-is.
	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *NginxResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan NginxResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(plan.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error updating nginx server state", err.Error())
		return
	}

	plan.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *NginxResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Restore Dokku's default of a running nginx server when this resource
	// is no longer managed, mirroring how other global toggle/config
	// resources reset to their default on delete.
	if err := r.apply(true); err != nil {
		resp.Diagnostics.AddError("Error restarting nginx server", err.Error())
	}
}

func (r *NginxResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
