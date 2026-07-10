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

// AppProxyResource models the proxy implementation selected for a Dokku app
// (`dokku proxy:set <app> type <value>`).
type AppProxyResource struct {
	client *dokku.Client
}

type AppProxyResourceModel struct {
	App  types.String `tfsdk:"app"`
	Type types.String `tfsdk:"type"`
	ID   types.String `tfsdk:"id"`
}

func (r *AppProxyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_proxy"
}

func (r *AppProxyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sets the proxy implementation used to route traffic to a Dokku app (`dokku proxy:set <app> type`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "Proxy implementation to use for this app, e.g. nginx, caddy, haproxy, traefik, openresty. Falls back to the global default (nginx unless overridden) when unset.",
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

func (r *AppProxyResource) set(app, value string) error {
	_, err := r.client.RunChecked("proxy:set", app, "type", value)
	return err
}

func (r *AppProxyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(data.App.ValueString(), data.Type.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error setting app proxy type", err.Error())
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

	report, err := r.client.Report("proxy", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Type = types.StringValue(report["type"])
	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppProxyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data AppProxyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(data.App.ValueString(), data.Type.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error updating app proxy type", err.Error())
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

	if _, err := r.client.RunChecked("proxy:set", data.App.ValueString(), "type"); err != nil {
		resp.Diagnostics.AddError("Error clearing app proxy type", err.Error())
	}
}

func (r *AppProxyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
