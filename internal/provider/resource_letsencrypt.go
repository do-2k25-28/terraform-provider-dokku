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
	_ resource.Resource                = &LetsencryptResource{}
	_ resource.ResourceWithConfigure   = &LetsencryptResource{}
	_ resource.ResourceWithImportState = &LetsencryptResource{}
)

func NewLetsencryptResource() resource.Resource { return &LetsencryptResource{} }

// LetsencryptResource models the global configuration of the
// dokku-letsencrypt plugin (`dokku letsencrypt:set --global <property>
// <value>`), used as the fallback for any app that doesn't override a
// property itself (see AppLetsencryptResource). Requires the letsencrypt
// plugin to be installed on the target host.
type LetsencryptResource struct {
	client *dokku.Client
}

type LetsencryptResourceModel struct {
	Email             types.String `tfsdk:"email"`
	DNSProvider       types.String `tfsdk:"dns_provider"`
	DNSProviderConfig types.Map    `tfsdk:"dns_provider_config"`
	ID                types.String `tfsdk:"id"`
}

func (r *LetsencryptResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_letsencrypt"
}

func (r *LetsencryptResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages global configuration for the dokku-letsencrypt plugin (`dokku letsencrypt:set --global`), used as the default for every app unless overridden per-app.",
		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				Optional:    true,
				Description: "Default contact email used for ACME registration/renewal notices.",
			},
			"dns_provider": schema.StringAttribute{
				Optional:    true,
				Description: "Default lego DNS provider used for DNS-01 challenges (enables wildcard certificates). Leave unset to use the HTTP-01 challenge by default.",
			},
			"dns_provider_config": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Sensitive:   true,
				Description: "Map of lego DNS provider credentials/options for `dns_provider`, e.g. `{ CLOUDFLARE_API_TOKEN = \"...\" }`. Keys are set as global `dns-provider-<KEY>` properties (`dokku letsencrypt:set --global dns-provider-<KEY> <value>`).",
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

func (r *LetsencryptResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// setGlobal sets a global letsencrypt property, or clears it when value is
// empty (`dokku letsencrypt:set --global <key>` with no value deletes it).
func (r *LetsencryptResource) setGlobal(key, value string) error {
	args := []string{"letsencrypt:set", "--global", key}
	if value != "" {
		args = append(args, value)
	}
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *LetsencryptResource) setDNSProviderConfig(config map[string]string) error {
	for key, value := range config {
		if err := r.setGlobal("dns-provider-"+key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *LetsencryptResource) unsetDNSProviderConfig(keys []string) error {
	for _, key := range keys {
		if err := r.setGlobal("dns-provider-"+key, ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *LetsencryptResource) apply(data LetsencryptResourceModel) error {
	if err := r.setGlobal("email", data.Email.ValueString()); err != nil {
		return err
	}
	return r.setGlobal("dns-provider", data.DNSProvider.ValueString())
}

func (r *LetsencryptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var dnsProviderConfig map[string]string
	resp.Diagnostics.Append(data.DNSProviderConfig.ElementsAs(ctx, &dnsProviderConfig, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.apply(data); err != nil {
		resp.Diagnostics.AddError("Error setting global letsencrypt configuration", err.Error())
		return
	}
	if err := r.setDNSProviderConfig(dnsProviderConfig); err != nil {
		resp.Diagnostics.AddError("Error setting global letsencrypt dns-provider config", err.Error())
		return
	}

	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LetsencryptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report("letsencrypt", "--global")
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Email = types.StringValue(report["global-email"])
	data.DNSProvider = types.StringValue(report["global-dns-provider"])

	dnsProviderConfig := make(map[string]string)
	const prefix = "global-dns-provider-"
	for key, value := range report {
		if name, ok := strings.CutPrefix(key, prefix); ok {
			dnsProviderConfig[name] = value
		}
	}
	if len(dnsProviderConfig) == 0 {
		data.DNSProviderConfig = types.MapNull(types.StringType)
	} else {
		dnsProviderConfigValue, diags := types.MapValueFrom(ctx, types.StringType, dnsProviderConfig)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		data.DNSProviderConfig = dnsProviderConfigValue
	}

	data.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LetsencryptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan LetsencryptResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state LetsencryptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var planConfig, stateConfig map[string]string
	resp.Diagnostics.Append(plan.DNSProviderConfig.ElementsAs(ctx, &planConfig, false)...)
	resp.Diagnostics.Append(state.DNSProviderConfig.ElementsAs(ctx, &stateConfig, false)...)
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

	if err := r.apply(plan); err != nil {
		resp.Diagnostics.AddError("Error updating global letsencrypt configuration", err.Error())
		return
	}
	if err := r.unsetDNSProviderConfig(toUnset); err != nil {
		resp.Diagnostics.AddError("Error clearing global letsencrypt dns-provider config", err.Error())
		return
	}
	if err := r.setDNSProviderConfig(toSet); err != nil {
		resp.Diagnostics.AddError("Error updating global letsencrypt dns-provider config", err.Error())
		return
	}

	plan.ID = types.StringValue("global")
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *LetsencryptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setGlobal("email", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing global letsencrypt email", err.Error())
	}
	if err := r.setGlobal("dns-provider", ""); err != nil {
		resp.Diagnostics.AddError("Error clearing global letsencrypt dns-provider", err.Error())
	}

	var config map[string]string
	resp.Diagnostics.Append(data.DNSProviderConfig.ElementsAs(ctx, &config, false)...)
	if resp.Diagnostics.HasError() {
		return
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	if err := r.unsetDNSProviderConfig(keys); err != nil {
		resp.Diagnostics.AddError("Error clearing global letsencrypt dns-provider config", err.Error())
	}
}

func (r *LetsencryptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
