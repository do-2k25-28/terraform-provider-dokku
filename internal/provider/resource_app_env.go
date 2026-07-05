package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &AppEnvResource{}
	_ resource.ResourceWithConfigure = &AppEnvResource{}
)

func NewAppEnvResource() resource.Resource { return &AppEnvResource{} }

// AppEnvResource models a single environment variable set on a Dokku app
// (`dokku config:set` / `dokku config:unset`).
type AppEnvResource struct {
	client *dokku.Client
}

type AppEnvResourceModel struct {
	App   types.String `tfsdk:"app"`
	Key   types.String `tfsdk:"key"`
	Value types.String `tfsdk:"value"`
	ID    types.String `tfsdk:"id"`
}

func (r *AppEnvResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_env"
}

func (r *AppEnvResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single environment variable on a Dokku app (`dokku config:set`). The value is passed unquoted over the SSH forced-command interface, so it may not contain whitespace.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"key": schema.StringAttribute{
				Required:    true,
				Description: "Environment variable name.",
			},
			"value": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "Environment variable value.",
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

func (r *AppEnvResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppEnvResource) set(app, key, value string) error {
	_, err := r.client.RunChecked("config:set", app, key+"="+value)
	return err
}

func (r *AppEnvResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	key := data.Key.ValueString()
	if err := r.set(app, key, data.Value.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error setting app env var", err.Error())
		return
	}

	data.ID = types.StringValue(app + ":" + key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppEnvResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	key := data.Key.ValueString()
	res, err := r.client.Run("config:get", app, key)
	if err != nil {
		resp.Diagnostics.AddError("Error reading app env var", err.Error())
		return
	}
	if res.ExitCode != 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Value = types.StringValue(strings.TrimRight(res.Stdout, "\n"))
	data.ID = types.StringValue(app + ":" + key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppEnvResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppEnvResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppEnvResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()
	oldKey := state.Key.ValueString()
	newKey := plan.Key.ValueString()

	if oldKey != newKey {
		if _, err := r.client.RunChecked("config:unset", app, oldKey); err != nil {
			resp.Diagnostics.AddError("Error unsetting old app env var", err.Error())
			return
		}
	}

	if err := r.set(app, newKey, plan.Value.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error updating app env var", err.Error())
		return
	}

	plan.ID = types.StringValue(app + ":" + newKey)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppEnvResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked("config:unset", data.App.ValueString(), data.Key.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error unsetting app env var", err.Error())
	}
}
