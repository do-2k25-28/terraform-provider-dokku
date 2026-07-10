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
	_ resource.Resource                = &TraefikResource{}
	_ resource.ResourceWithConfigure   = &TraefikResource{}
	_ resource.ResourceWithImportState = &TraefikResource{}
)

func NewTraefikResource() resource.Resource { return &TraefikResource{} }

// TraefikResource controls whether Dokku's global traefik proxy is running
// (`dokku traefik:start` / `dokku traefik:stop`). Dokku exposes no status
// query for this, so state is trusted rather than verified on Read.
type TraefikResource struct {
	client *dokku.Client
}

type TraefikResourceModel struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	ID      types.String `tfsdk:"id"`
}

func (r *TraefikResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_traefik"
}

func (r *TraefikResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Controls whether the global Dokku traefik proxy is running (`dokku traefik:start` / `dokku traefik:stop`).",
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether the traefik proxy should be running. `true` runs `dokku traefik:start`, `false` runs `dokku traefik:stop`.",
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

func (r *TraefikResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *TraefikResource) apply(enabled bool) error {
	if enabled {
		_, err := r.client.RunChecked("traefik:start")
		return err
	}
	_, err := r.client.RunChecked("traefik:stop")
	return err
}

func (r *TraefikResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data TraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error setting traefik proxy state", err.Error())
		return
	}

	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TraefikResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data TraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Dokku has no command to query whether the global traefik proxy is
	// currently running, so the last-applied state is trusted as-is.
	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TraefikResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(plan.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error updating traefik proxy state", err.Error())
		return
	}

	plan.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TraefikResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Unlike nginx, traefik is an opt-in proxy that Dokku does not run by
	// default, so restoring the default when this resource is no longer
	// managed means stopping it rather than starting it.
	if err := r.apply(false); err != nil {
		resp.Diagnostics.AddError("Error stopping traefik proxy", err.Error())
	}
}

func (r *TraefikResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
