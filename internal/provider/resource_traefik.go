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
	_ resource.Resource                = &TraefikResource{}
	_ resource.ResourceWithConfigure   = &TraefikResource{}
	_ resource.ResourceWithImportState = &TraefikResource{}
)

func NewTraefikResource() resource.Resource { return &TraefikResource{} }

// TraefikResource controls whether Dokku's global traefik proxy is running
// (`dokku traefik:start` / `dokku traefik:stop`) and its letsencrypt
// integration (`dokku traefik:set --global <property> <value>`). Dokku
// exposes no status query for the running state, so that part of state is
// trusted rather than verified on Read; the letsencrypt-* properties are
// read back via `traefik:report --global`.
type TraefikResource struct {
	client *dokku.Client
}

type TraefikResourceModel struct {
	Enabled                      types.Bool   `tfsdk:"enabled"`
	LetsencryptEmail             types.String `tfsdk:"letsencrypt_email"`
	LetsencryptServer            types.String `tfsdk:"letsencrypt_server"`
	LetsencryptChallengeMode     types.String `tfsdk:"letsencrypt_challenge_mode"`
	LetsencryptDNSProvider       types.String `tfsdk:"letsencrypt_dns_provider"`
	LetsencryptDNSProviderConfig types.Map    `tfsdk:"letsencrypt_dns_provider_config"`
	ID                           types.String `tfsdk:"id"`
}

func (r *TraefikResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_traefik"
}

func (r *TraefikResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Controls whether the global Dokku traefik proxy is running (`dokku traefik:start` / `dokku traefik:stop`) and its letsencrypt integration.",
		Attributes: map[string]schema.Attribute{
			"enabled": schema.BoolAttribute{
				Required:    true,
				Description: "Whether the traefik proxy should be running. `true` runs `dokku traefik:start`, `false` runs `dokku traefik:stop`.",
			},
			"letsencrypt_email": schema.StringAttribute{
				Optional:    true,
				Description: "Contact email that enables the traefik letsencrypt integration (`dokku traefik:set --global letsencrypt-email`). Letsencrypt is disabled and https port mappings are ignored while this is unset. Changing this or any other `letsencrypt_*` attribute requires restarting the Traefik container (toggle `enabled`) to take effect.",
			},
			"letsencrypt_server": schema.StringAttribute{
				Optional:    true,
				Description: "ACME directory URL used when requesting certificates (`dokku traefik:set --global letsencrypt-server`). Defaults to the production Let's Encrypt server.",
			},
			"letsencrypt_challenge_mode": schema.StringAttribute{
				Optional:    true,
				Description: "ACME challenge method used by Traefik: `tls`, `http`, or `dns` (`dokku traefik:set --global challenge-mode`). Defaults to `tls`.",
			},
			"letsencrypt_dns_provider": schema.StringAttribute{
				Optional:    true,
				Description: "Lego DNS provider name used when `letsencrypt_challenge_mode` is `dns` (`dokku traefik:set --global dns-provider`).",
			},
			"letsencrypt_dns_provider_config": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Map of lego DNS provider credentials/options for `letsencrypt_dns_provider`, e.g. `{ CLOUDFLARE_API_TOKEN = \"...\" }`. Keys are set as `dns-provider-<KEY>` properties (`dokku traefik:set --global dns-provider-<KEY> <value>`).",
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

// setGlobal sets a global traefik property, or clears it when value is
// empty (`dokku traefik:set --global <key>` with no value deletes it).
func (r *TraefikResource) setGlobal(key, value string) error {
	args := []string{"traefik:set", "--global", key}
	if value != "" {
		args = append(args, value)
	}
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *TraefikResource) setDNSProviderConfig(config map[string]string) error {
	for key, value := range config {
		if err := r.setGlobal("dns-provider-"+key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *TraefikResource) unsetDNSProviderConfig(keys []string) error {
	for _, key := range keys {
		if err := r.setGlobal("dns-provider-"+key, ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *TraefikResource) applyLetsencrypt(data TraefikResourceModel) error {
	if err := r.setGlobal("letsencrypt-email", data.LetsencryptEmail.ValueString()); err != nil {
		return err
	}
	if err := r.setGlobal("letsencrypt-server", data.LetsencryptServer.ValueString()); err != nil {
		return err
	}
	if err := r.setGlobal("challenge-mode", data.LetsencryptChallengeMode.ValueString()); err != nil {
		return err
	}
	return r.setGlobal("dns-provider", data.LetsencryptDNSProvider.ValueString())
}

func (r *TraefikResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data TraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dnsProviderConfig map[string]string
	resp.Diagnostics.Append(data.LetsencryptDNSProviderConfig.ElementsAs(ctx, &dnsProviderConfig, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.applyLetsencrypt(data); err != nil {
		resp.Diagnostics.AddError("Error setting traefik letsencrypt configuration", err.Error())
		return
	}
	if err := r.setDNSProviderConfig(dnsProviderConfig); err != nil {
		resp.Diagnostics.AddError("Error setting traefik letsencrypt dns-provider config", err.Error())
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

	report, err := r.client.Report("traefik", "--global")
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.LetsencryptEmail = types.StringValue(report["global-letsencrypt-email"])
	data.LetsencryptServer = types.StringValue(report["global-letsencrypt-server"])
	data.LetsencryptChallengeMode = types.StringValue(report["global-challenge-mode"])
	data.LetsencryptDNSProvider = types.StringValue(report["global-dns-provider"])

	dnsProviderConfig := make(map[string]string)
	const prefix = "global-dns-provider-"
	for key, value := range report {
		if name, ok := strings.CutPrefix(key, prefix); ok {
			dnsProviderConfig[name] = value
		}
	}
	if len(dnsProviderConfig) == 0 {
		data.LetsencryptDNSProviderConfig = types.MapNull(types.StringType)
	} else {
		dnsProviderConfigValue, diags := types.MapValueFrom(ctx, types.StringType, dnsProviderConfig)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		data.LetsencryptDNSProviderConfig = dnsProviderConfigValue
	}

	// Dokku has no command to query whether the global traefik proxy is
	// currently running, so the last-applied Enabled state is trusted as-is.
	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *TraefikResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TraefikResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state TraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var planConfig, stateConfig map[string]string
	resp.Diagnostics.Append(plan.LetsencryptDNSProviderConfig.ElementsAs(ctx, &planConfig, false)...)
	resp.Diagnostics.Append(state.LetsencryptDNSProviderConfig.ElementsAs(ctx, &stateConfig, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var toUnset []string
	for key := range stateConfig {
		if _, ok := planConfig[key]; !ok {
			toUnset = append(toUnset, key)
		}
	}

	toSet := make(map[string]string)
	for key, value := range planConfig {
		if old, ok := stateConfig[key]; !ok || old != value {
			toSet[key] = value
		}
	}

	if err := r.applyLetsencrypt(plan); err != nil {
		resp.Diagnostics.AddError("Error updating traefik letsencrypt configuration", err.Error())
		return
	}
	if err := r.unsetDNSProviderConfig(toUnset); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik letsencrypt dns-provider config", err.Error())
		return
	}
	if err := r.setDNSProviderConfig(toSet); err != nil {
		resp.Diagnostics.AddError("Error updating traefik letsencrypt dns-provider config", err.Error())
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
	var data TraefikResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setGlobal("letsencrypt-email", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik letsencrypt email", err.Error())
	}
	if err := r.setGlobal("letsencrypt-server", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik letsencrypt server", err.Error())
	}
	if err := r.setGlobal("challenge-mode", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik challenge mode", err.Error())
	}
	if err := r.setGlobal("dns-provider", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik dns-provider", err.Error())
	}

	var config map[string]string
	resp.Diagnostics.Append(data.LetsencryptDNSProviderConfig.ElementsAs(ctx, &config, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	if err := r.unsetDNSProviderConfig(keys); err != nil {
		resp.Diagnostics.AddError("Error clearing traefik letsencrypt dns-provider config", err.Error())
	}

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
