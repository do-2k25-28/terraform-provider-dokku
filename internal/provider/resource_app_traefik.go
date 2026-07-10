package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource                = &AppTraefikResource{}
	_ resource.ResourceWithConfigure   = &AppTraefikResource{}
	_ resource.ResourceWithImportState = &AppTraefikResource{}
)

func NewAppTraefikResource() resource.Resource { return &AppTraefikResource{} }

// AppTraefikResource models the custom Traefik container labels set on a
// Dokku app (`dokku traefik:labels:add` / `dokku traefik:labels:remove`).
// Labels are injected into the app's containers on the next deploy or
// rebuild.
type AppTraefikResource struct {
	client *dokku.Client
}

type AppTraefikResourceModel struct {
	App    types.String `tfsdk:"app"`
	Labels types.Map    `tfsdk:"labels"`
	ID     types.String `tfsdk:"id"`
}

func (r *AppTraefikResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_traefik"
}

func (r *AppTraefikResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages custom Traefik container labels on a Dokku app (`dokku traefik:labels:add` / `dokku traefik:labels:remove`). A deploy or rebuild (`dokku ps:rebuild`) is required for label changes to take effect on running containers.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"labels": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of Traefik container label names to values, e.g. `{ \"traefik.http.routers.web.middlewares\" = \"my-middleware\" }`. Values are passed unquoted over the SSH forced-command interface, so they may not contain whitespace.",
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

func (r *AppTraefikResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// set adds or overwrites a single Traefik label. traefik:labels:add only
// accepts one label per invocation, unlike e.g. config:set.
func (r *AppTraefikResource) set(app string, labels map[string]string) error {
	for name, value := range labels {
		if _, err := r.client.RunChecked("traefik:labels:add", app, name, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *AppTraefikResource) unset(app string, names []string) error {
	for _, name := range names {
		if _, err := r.client.RunChecked("traefik:labels:remove", app, name); err != nil {
			return err
		}
	}
	return nil
}

func (r *AppTraefikResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppTraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(app, labels); err != nil {
		resp.Diagnostics.AddError("Error setting app traefik labels", err.Error())
		return
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppTraefikResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppTraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current := make(map[string]string, len(labels))
	for name := range labels {
		res, err := r.client.Run("traefik:labels:show", app, name)
		if err != nil {
			resp.Diagnostics.AddError("Error reading app traefik label", err.Error())
			return
		}
		if res.ExitCode != 0 {
			resp.State.RemoveResource(ctx)
			return
		}
		value := strings.TrimRight(res.Stdout, "\n")
		if value == "" {
			// Label was removed outside of Terraform; drop it from state so
			// the plan shows it as needing to be re-added.
			continue
		}
		current[name] = value
	}

	labelsValue, diags := types.MapValueFrom(ctx, types.StringType, current)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Labels = labelsValue
	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppTraefikResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppTraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppTraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()
	var planLabels, stateLabels map[string]string
	resp.Diagnostics.Append(plan.Labels.ElementsAs(ctx, &planLabels, false)...)
	resp.Diagnostics.Append(state.Labels.ElementsAs(ctx, &stateLabels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var toUnset []string
	for name := range stateLabels {
		if _, ok := planLabels[name]; !ok {
			toUnset = append(toUnset, name)
		}
	}

	toSet := make(map[string]string)
	for name, value := range planLabels {
		if old, ok := stateLabels[name]; !ok || old != value {
			toSet[name] = value
		}
	}

	if err := r.unset(app, toUnset); err != nil {
		resp.Diagnostics.AddError("Error removing app traefik labels", err.Error())
		return
	}
	if err := r.set(app, toSet); err != nil {
		resp.Diagnostics.AddError("Error updating app traefik labels", err.Error())
		return
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppTraefikResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppTraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}

	if err := r.unset(data.App.ValueString(), names); err != nil {
		resp.Diagnostics.AddError("Error removing app traefik labels", err.Error())
	}
}

func (r *AppTraefikResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
