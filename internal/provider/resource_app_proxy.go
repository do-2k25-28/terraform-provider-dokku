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
	_ resource.Resource                = &AppProxyResource{}
	_ resource.ResourceWithConfigure   = &AppProxyResource{}
	_ resource.ResourceWithImportState = &AppProxyResource{}
)

func NewAppProxyResource() resource.Resource { return &AppProxyResource{} }

// AppProxyResource models whether the proxy is enabled for a Dokku app
// (`dokku proxy:enable <app>` / `dokku proxy:disable <app>`).
type AppProxyResource struct {
	client *dokku.Client
}

type AppProxyResourceModel struct {
	App     types.String `tfsdk:"app"`
	Enabled types.Bool   `tfsdk:"enabled"`
	ID      types.String `tfsdk:"id"`
}

func (r *AppProxyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_proxy"
}

func (r *AppProxyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Enables or disables the proxy for a Dokku app (`dokku proxy:enable <app>` / `dokku proxy:disable <app>`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether the proxy is enabled for this app. `true` runs `dokku proxy:enable`, `false` runs `dokku proxy:disable`.",
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

func (r *AppProxyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppProxyResource) apply(ctx context.Context, app string, enabled bool) error {
	if enabled {
		_, err := r.client.RunChecked(ctx, "proxy:enable", app)
		return err
	}
	_, err := r.client.RunChecked(ctx, "proxy:disable", app)
	return err
}

func (r *AppProxyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(ctx, data.App.ValueString(), data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error setting app proxy state", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppProxyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report(ctx, "proxy", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Enabled = types.BoolValue(report["enabled"] == "true")
	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppProxyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(ctx, data.App.ValueString(), data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error updating app proxy state", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppProxyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(ctx, data.App.ValueString(), true); err != nil {
		resp.Diagnostics.AddError("Error restoring app proxy state", err.Error())
	}
}

func (r *AppProxyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
