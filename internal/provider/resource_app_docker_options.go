package provider

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &AppDockerOptionsResource{}
	_ resource.ResourceWithConfigure = &AppDockerOptionsResource{}
)

func NewAppDockerOptionsResource() resource.Resource { return &AppDockerOptionsResource{} }

// AppDockerOptionsResource models a single custom `docker run`/`docker
// build` flag injected into an app's containers (`dokku docker-options:add`
// / `dokku docker-options:remove`). An option applies to one or more
// lifecycle phases (build, deploy, run) and, for the deploy phase only, can
// be scoped to specific Procfile process types via `processes`.
type AppDockerOptionsResource struct {
	client *dokku.Client
}

type AppDockerOptionsResourceModel struct {
	App       types.String `tfsdk:"app"`
	Phases    types.List   `tfsdk:"phases"`
	Processes types.List   `tfsdk:"processes"`
	Option    types.String `tfsdk:"option"`
	ID        types.String `tfsdk:"id"`
}

func (r *AppDockerOptionsResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_docker_options"
}

func (r *AppDockerOptionsResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Injects a single custom Docker CLI flag into an app's build, deploy, and/or run containers (`dokku docker-options:add`), e.g. `--memory 1g` or `--build-arg NODE_ENV=production`. See https://dokku.com/docs/advanced-usage/docker-options/.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"phases": schema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Container lifecycle phases the option applies to: build, deploy, run.",
			},
			"processes": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Procfile process types (e.g. web, worker) to scope the option to, instead of every process type in the app. Only supported when phases is [\"deploy\"].",
			},
			"option": schema.StringAttribute{
				Required:    true,
				Description: "A single Docker CLI flag to inject, e.g. \"--memory 1g\" or \"--build-arg NODE_ENV=production\". Must be one flag (and its value, if any); add a separate resource instance per flag. Passed unquoted over SSH, so the value may not contain multiple consecutive spaces.",
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

func (r *AppDockerOptionsResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// dockerOptionsID uniquely identifies a docker-options resource instance,
// mirroring how AppDomainResource/AppPortResource key state on the full spec
// rather than a single Dokku-assigned identifier.
func dockerOptionsID(app string, processes, phases []string, option string) string {
	return app + ":" + strings.Join(processes, ",") + ":" + strings.Join(phases, ",") + ":" + option
}

// splitOptionArgs breaks a single "--flag value" option into separate SSH
// arguments. Dokku's forced-command interface joins argv with a single space
// and does no quote removal (see dokku.joinArgs), so this is the only way to
// transport a value containing whitespace; the docker-options:add/:remove
// subcommands re-join their trailing positional args with a single space
// server-side, reconstructing the original flag.
func splitOptionArgs(option string) []string {
	return strings.Fields(option)
}

func (r *AppDockerOptionsResource) add(app string, processes, phases []string, option string) error {
	args := []string{"docker-options:add"}
	for _, p := range processes {
		args = append(args, "--process", p)
	}
	args = append(args, app, strings.Join(phases, ","))
	args = append(args, splitOptionArgs(option)...)
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *AppDockerOptionsResource) remove(app string, processes, phases []string, option string) error {
	args := []string{"docker-options:remove"}
	for _, p := range processes {
		args = append(args, "--process", p)
	}
	args = append(args, app, strings.Join(phases, ","))
	args = append(args, splitOptionArgs(option)...)
	_, err := r.client.RunChecked(args...)
	return err
}

// dockerOptionsReport runs `docker-options:report <app> --format json` and
// extracts every "<key>-list" field as a string slice. The report mixes flat
// string fields with these list fields (the raw stored options per phase,
// and per "deploy.<process>" for process-scoped options), so it can't go
// through the shared Client.Report helper, which stringifies every value.
func (r *AppDockerOptionsResource) dockerOptionsReport(app string) (map[string][]string, error) {
	res, err := r.client.RunChecked("docker-options:report", app, "--format", "json")
	if err != nil {
		return nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &raw); err != nil {
		return nil, err
	}

	lists := make(map[string][]string, len(raw))
	for key, value := range raw {
		arr, ok := value.([]any)
		if !ok {
			continue
		}
		list := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				list = append(list, s)
			}
		}
		lists[key] = list
	}
	return lists, nil
}

// dockerOptionsReportKeys maps a (processes, phases) scope to the report's
// "<key>-list" keys: "<phase>-list" for the default (all processes) scope,
// or "deploy.<process>-list" per process for process-scoped options (Dokku
// only supports process scoping on the deploy phase).
func dockerOptionsReportKeys(processes, phases []string) []string {
	if len(processes) == 0 {
		keys := make([]string, len(phases))
		for i, phase := range phases {
			keys[i] = phase + "-list"
		}
		return keys
	}
	keys := make([]string, len(processes))
	for i, p := range processes {
		keys[i] = "deploy." + p + "-list"
	}
	return keys
}

// dockerOptionPresent reports whether option shows up in every report list
// the resource's (processes, phases) scope maps to.
func dockerOptionPresent(lists map[string][]string, processes, phases []string, option string) bool {
	keys := dockerOptionsReportKeys(processes, phases)
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if !slices.Contains(lists[key], option) {
			return false
		}
	}
	return true
}

func (r *AppDockerOptionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppDockerOptionsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	phases := stringListToGo(ctx, data.Phases)
	processes := stringListToGo(ctx, data.Processes)
	option := data.Option.ValueString()

	if err := r.add(app, processes, phases, option); err != nil {
		resp.Diagnostics.AddError("Error adding docker option", err.Error())
		return
	}

	data.ID = types.StringValue(dockerOptionsID(app, processes, phases, option))
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerOptionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppDockerOptionsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	phases := stringListToGo(ctx, data.Phases)
	processes := stringListToGo(ctx, data.Processes)
	option := data.Option.ValueString()

	lists, err := r.dockerOptionsReport(app)
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	if !dockerOptionPresent(lists, processes, phases, option) {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(dockerOptionsID(app, processes, phases, option))
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerOptionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppDockerOptionsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppDockerOptionsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()
	planPhases := stringListToGo(ctx, plan.Phases)
	planProcesses := stringListToGo(ctx, plan.Processes)
	planOption := plan.Option.ValueString()

	statePhases := stringListToGo(ctx, state.Phases)
	stateProcesses := stringListToGo(ctx, state.Processes)
	stateOption := state.Option.ValueString()

	if !equalStringSlices(planPhases, statePhases) || !equalStringSlices(planProcesses, stateProcesses) || planOption != stateOption {
		if err := r.remove(app, stateProcesses, statePhases, stateOption); err != nil {
			resp.Diagnostics.AddError("Error removing old docker option", err.Error())
			return
		}
		if err := r.add(app, planProcesses, planPhases, planOption); err != nil {
			resp.Diagnostics.AddError("Error adding new docker option", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(dockerOptionsID(app, planProcesses, planPhases, planOption))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppDockerOptionsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppDockerOptionsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	phases := stringListToGo(ctx, data.Phases)
	processes := stringListToGo(ctx, data.Processes)
	option := data.Option.ValueString()

	if err := r.remove(app, processes, phases, option); err != nil {
		resp.Diagnostics.AddError("Error removing docker option", err.Error())
	}
}
