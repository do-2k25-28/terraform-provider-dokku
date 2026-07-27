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
	_ resource.Resource                = &AppChecksResource{}
	_ resource.ResourceWithConfigure   = &AppChecksResource{}
	_ resource.ResourceWithImportState = &AppChecksResource{}
)

func NewAppChecksResource() resource.Resource { return &AppChecksResource{} }

// AppChecksResource models whether zero-downtime deployment checks are
// enabled for a Dokku app (`dokku checks:enable <app>` / `dokku checks:disable <app>`).
type AppChecksResource struct {
	client *dokku.Client
}

type AppChecksResourceModel struct {
	App     types.String `tfsdk:"app"`
	Enabled types.Bool   `tfsdk:"enabled"`
	ID      types.String `tfsdk:"id"`
}

func (r *AppChecksResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_checks"
}

func (r *AppChecksResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Enables or disables zero-downtime deployment checks for a Dokku app (`dokku checks:enable <app>` / `dokku checks:disable <app>`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether zero-downtime deployment checks are enabled for this app. `true` runs `dokku checks:enable`, `false` runs `dokku checks:disable`, both across all process types.",
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

func (r *AppChecksResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppChecksResource) apply(app string, enabled bool) error {
	if enabled {
		_, err := r.client.RunChecked("checks:enable", app)
		return err
	}
	_, err := r.client.RunChecked("checks:disable", app)
	return err
}

func (r *AppChecksResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppChecksResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data.App.ValueString(), data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error setting app checks state", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppChecksResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppChecksResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report("checks", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	disabledList := report["disabled-list"]
	data.Enabled = types.BoolValue(disabledList == "" || disabledList == "none")
	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppChecksResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data AppChecksResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data.App.ValueString(), data.Enabled.ValueBool()); err != nil {
		resp.Diagnostics.AddError("Error updating app checks state", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppChecksResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppChecksResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data.App.ValueString(), true); err != nil {
		resp.Diagnostics.AddError("Error restoring app checks state", err.Error())
	}
}

func (r *AppChecksResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
