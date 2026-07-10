package provider

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &PluginsResource{}
	_ resource.ResourceWithConfigure = &PluginsResource{}
)

func NewPluginsResource() resource.Resource { return &PluginsResource{} }

// PluginsResource models third-party Dokku plugins installed from git
// (`dokku plugin:install <url> --name <name>` / `dokku plugin:uninstall
// <name>`), keyed by plugin name with the git URL to install as the value.
type PluginsResource struct {
	client *dokku.Client
}

type PluginsResourceModel struct {
	Plugins types.Map    `tfsdk:"plugins"`
	ID      types.String `tfsdk:"id"`
}

func (r *PluginsResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugins"
}

func (r *PluginsResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages third-party Dokku plugins installed from git (`dokku plugin:install` / `dokku plugin:uninstall`).",
		Attributes: map[string]schema.Attribute{
			"plugins": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of plugin name to the git URL to install it from, e.g. `{ postgres = \"https://github.com/dokku/dokku-postgres.git\" }`.",
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

func (r *PluginsResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *PluginsResource) install(name, url string) error {
	_, err := r.client.RunChecked("plugin:install", url, "--name", name)
	return err
}

func (r *PluginsResource) uninstall(name string) error {
	_, err := r.client.RunChecked("plugin:uninstall", name)
	return err
}

// sourceURLs returns the git source URL Dokku recorded for every currently
// installed plugin (`dokku plugin:list --format json`), keyed by plugin
// name. Core plugins and any plugin not installed from git have an empty
// source URL.
func (r *PluginsResource) sourceURLs() (map[string]string, error) {
	res, err := r.client.RunChecked("plugin:list", "--format", "json")
	if err != nil {
		return nil, err
	}

	var entries []struct {
		Name      string `json:"name"`
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &entries); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		out[entry.Name] = entry.SourceURL
	}
	return out, nil
}

func (r *PluginsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PluginsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plugins map[string]string
	resp.Diagnostics.Append(data.Plugins.ElementsAs(ctx, &plugins, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := r.install(name, plugins[name]); err != nil {
			resp.Diagnostics.AddError("Error installing plugin", err.Error())
			return
		}
	}

	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PluginsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PluginsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plugins map[string]string
	resp.Diagnostics.Append(data.Plugins.ElementsAs(ctx, &plugins, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.sourceURLs()
	if err != nil {
		resp.Diagnostics.AddError("Error reading installed plugins", err.Error())
		return
	}

	updated := make(map[string]string, len(plugins))
	for name := range plugins {
		if url, ok := current[name]; ok {
			// Plugin was uninstalled outside of Terraform if this is
			// missing; drop it from state so the plan shows it as needing
			// to be reinstalled.
			updated[name] = url
		}
	}

	pluginsValue, diags := types.MapValueFrom(ctx, types.StringType, updated)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Plugins = pluginsValue
	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PluginsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PluginsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state PluginsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var planPlugins, statePlugins map[string]string
	resp.Diagnostics.Append(plan.Plugins.ElementsAs(ctx, &planPlugins, false)...)
	resp.Diagnostics.Append(state.Plugins.ElementsAs(ctx, &statePlugins, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var toUninstall []string
	for name := range statePlugins {
		if _, ok := planPlugins[name]; !ok {
			toUninstall = append(toUninstall, name)
		}
	}
	sort.Strings(toUninstall)

	toInstall := make(map[string]string)
	for name, url := range planPlugins {
		if old, ok := statePlugins[name]; !ok || old != url {
			toInstall[name] = url
		}
	}
	installNames := make([]string, 0, len(toInstall))
	for name := range toInstall {
		installNames = append(installNames, name)
	}
	sort.Strings(installNames)

	for _, name := range toUninstall {
		if err := r.uninstall(name); err != nil {
			resp.Diagnostics.AddError("Error uninstalling plugin", err.Error())
			return
		}
	}
	for _, name := range installNames {
		// A changed URL for an already-installed plugin name has no direct
		// "update source" command, so re-point it by uninstalling and
		// reinstalling from the new URL.
		if _, existed := statePlugins[name]; existed {
			if err := r.uninstall(name); err != nil {
				resp.Diagnostics.AddError("Error uninstalling plugin", err.Error())
				return
			}
		}
		if err := r.install(name, toInstall[name]); err != nil {
			resp.Diagnostics.AddError("Error installing plugin", err.Error())
			return
		}
	}

	plan.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *PluginsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PluginsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var plugins map[string]string
	resp.Diagnostics.Append(data.Plugins.ElementsAs(ctx, &plugins, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := r.uninstall(name); err != nil {
			resp.Diagnostics.AddError("Error uninstalling plugin", err.Error())
		}
	}
}
