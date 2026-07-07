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

// AppEnvResource models the environment variables set on a Dokku app
// (`dokku config:set` / `dokku config:unset`).
type AppEnvResource struct {
	client *dokku.Client
}

type AppEnvResourceModel struct {
	App types.String `tfsdk:"app"`
	Env types.Map    `tfsdk:"env"`
	ID  types.String `tfsdk:"id"`
}

func (r *AppEnvResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_env"
}

func (r *AppEnvResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the environment variables on a Dokku app (`dokku config:set`). Values are passed unquoted over the SSH forced-command interface, so they may not contain whitespace.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"env": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Map of environment variable names to values.",
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

func (r *AppEnvResource) set(app string, env map[string]string) error {
	if len(env) == 0 {
		return nil
	}
	args := []string{"config:set", app}
	for k, v := range env {
		args = append(args, k+"="+v)
	}
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *AppEnvResource) unset(app string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	args := append([]string{"config:unset", app}, keys...)
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *AppEnvResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var env map[string]string
	resp.Diagnostics.Append(data.Env.ElementsAs(ctx, &env, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(app, env); err != nil {
		resp.Diagnostics.AddError("Error setting app env vars", err.Error())
		return
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppEnvResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var env map[string]string
	resp.Diagnostics.Append(data.Env.ElementsAs(ctx, &env, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current := make(map[string]string, len(env))
	for key := range env {
		res, err := r.client.Run("config:get", app, key)
		if err != nil {
			resp.Diagnostics.AddError("Error reading app env var", err.Error())
			return
		}
		if res.ExitCode != 0 {
			// Var was unset outside of Terraform; drop it from state so the
			// plan shows it as needing to be (re)created.
			continue
		}
		current[key] = strings.TrimRight(res.Stdout, "\n")
	}

	envValue, diags := types.MapValueFrom(ctx, types.StringType, current)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Env = envValue
	data.ID = types.StringValue(app)
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
	var planEnv, stateEnv map[string]string
	resp.Diagnostics.Append(plan.Env.ElementsAs(ctx, &planEnv, false)...)
	resp.Diagnostics.Append(state.Env.ElementsAs(ctx, &stateEnv, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var toUnset []string
	for key := range stateEnv {
		if _, ok := planEnv[key]; !ok {
			toUnset = append(toUnset, key)
		}
	}

	toSet := make(map[string]string)
	for key, value := range planEnv {
		if old, ok := stateEnv[key]; !ok || old != value {
			toSet[key] = value
		}
	}

	if err := r.unset(app, toUnset); err != nil {
		resp.Diagnostics.AddError("Error unsetting app env vars", err.Error())
		return
	}
	if err := r.set(app, toSet); err != nil {
		resp.Diagnostics.AddError("Error updating app env vars", err.Error())
		return
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppEnvResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppEnvResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var env map[string]string
	resp.Diagnostics.Append(data.Env.ElementsAs(ctx, &env, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}

	if err := r.unset(data.App.ValueString(), keys); err != nil {
		resp.Diagnostics.AddError("Error unsetting app env vars", err.Error())
	}
}
