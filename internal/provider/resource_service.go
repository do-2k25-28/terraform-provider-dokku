// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

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

// serviceResource is a generic implementation shared by Dokku's official
// datastore plugins (postgres, redis, ...), which are all generated from the
// same dokku-service-plugin skeleton and therefore expose an identical CLI:
// <plugin>:create/:destroy/:info/:exists/:link/:unlink/:links/:app-links/:upgrade.
type serviceResource struct {
	client *dokku.Client
	plugin string // e.g. "postgres", "redis"
}

type ServiceResourceModel struct {
	Name         types.String `tfsdk:"name"`
	ImageVersion types.String `tfsdk:"image_version"`
	Dsn          types.String `tfsdk:"dsn"`
	Status       types.String `tfsdk:"status"`
	ID           types.String `tfsdk:"id"`
}

var (
	_ resource.Resource                = &serviceResource{}
	_ resource.ResourceWithConfigure   = &serviceResource{}
	_ resource.ResourceWithImportState = &serviceResource{}
)

func (r *serviceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.plugin
}

func (r *serviceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Dokku " + r.plugin + " service (`dokku " + r.plugin + ":create`).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Service name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image_version": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Datastore image version/tag, e.g. \"16\". Changing this upgrades the service in place.",
			},
			"dsn": schema.StringAttribute{
				Computed:    true,
				Sensitive:   true,
				Description: "Connection string for the service.",
			},
			"status": schema.StringAttribute{
				Computed:    true,
				Description: "Current container status of the service.",
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

func (r *serviceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// parseServiceInfo parses the plain-text "Key:   Value" block printed by
// "<plugin>:info <name>" / "<plugin>:create <name>" into a lowercase,
// hyphenated key map, e.g. "Image version" -> "image-version".
func parseServiceInfo(output string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "=====>") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if val == "-" {
			val = ""
		}
		key = strings.ToLower(strings.ReplaceAll(key, " ", "-"))
		out[key] = val
	}
	return out
}

func (r *serviceResource) fetchInfo(name string) (map[string]string, error) {
	res, err := r.client.RunChecked(r.plugin+":info", name)
	if err != nil {
		return nil, err
	}
	return parseServiceInfo(res.Stdout), nil
}

func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ServiceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	args := []string{r.plugin + ":create", name}
	if !data.ImageVersion.IsNull() && !data.ImageVersion.IsUnknown() && data.ImageVersion.ValueString() != "" {
		args = append(args, "--image-version", data.ImageVersion.ValueString())
	}

	if _, err := r.client.RunChecked(args...); err != nil {
		resp.Diagnostics.AddError("Error creating "+r.plugin+" service", err.Error())
		return
	}

	data.ID = types.StringValue(name)
	r.populate(&data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serviceResource) populate(data *ServiceResourceModel) {
	info, err := r.fetchInfo(data.Name.ValueString())
	if err != nil {
		return
	}
	if v, ok := info["version"]; ok {
		// Value looks like "postgres:16" or "redis:7.2" - keep the tag only.
		if idx := strings.LastIndex(v, ":"); idx >= 0 {
			data.ImageVersion = types.StringValue(v[idx+1:])
		} else {
			data.ImageVersion = types.StringValue(v)
		}
	}
	data.Dsn = types.StringValue(info["dsn"])
	data.Status = types.StringValue(info["status"])
}

func (r *serviceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	res, err := r.client.Run(r.plugin+":exists", name)
	if err != nil {
		resp.Diagnostics.AddError("Error checking "+r.plugin+" service", err.Error())
		return
	}
	if res.ExitCode != 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(name)
	r.populate(&data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ServiceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	if plan.ImageVersion.ValueString() != state.ImageVersion.ValueString() && plan.ImageVersion.ValueString() != "" {
		if _, err := r.client.RunChecked(r.plugin+":upgrade", name, "--image-version", plan.ImageVersion.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error upgrading "+r.plugin+" service", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(name)
	r.populate(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ServiceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked(r.plugin+":destroy", data.Name.ValueString(), "--force"); err != nil {
		resp.Diagnostics.AddError("Error destroying "+r.plugin+" service", err.Error())
	}
}

func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}

// serviceLinkResource is the generic <plugin>:link/:unlink resource shared
// by postgres and redis (and any other dokku-service-plugin-based service).
type serviceLinkResource struct {
	client *dokku.Client
	plugin string
}

type ServiceLinkResourceModel struct {
	Service types.String `tfsdk:"service"`
	App     types.String `tfsdk:"app"`
	Alias   types.String `tfsdk:"alias"`
	ID      types.String `tfsdk:"id"`
}

var (
	_ resource.Resource              = &serviceLinkResource{}
	_ resource.ResourceWithConfigure = &serviceLinkResource{}
)

func (r *serviceLinkResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.plugin + "_link"
}

func (r *serviceLinkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Links a Dokku " + r.plugin + " service to an app (`dokku " + r.plugin + ":link`).",
		Attributes: map[string]schema.Attribute{
			"service": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"alias": schema.StringAttribute{
				Optional:    true,
				Description: "Optional env var alias prefix for the link (e.g. \"DB1\" yields DB1_URL instead of the plugin default).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
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

func (r *serviceLinkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *serviceLinkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ServiceLinkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	args := []string{r.plugin + ":link", data.Service.ValueString(), data.App.ValueString()}
	if !data.Alias.IsNull() && data.Alias.ValueString() != "" {
		args = append(args, "--alias", data.Alias.ValueString())
	}

	if _, err := r.client.RunChecked(args...); err != nil {
		resp.Diagnostics.AddError("Error linking "+r.plugin+" service", err.Error())
		return
	}

	data.ID = types.StringValue(data.Service.ValueString() + ":" + data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serviceLinkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ServiceLinkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.client.Run(r.plugin+":links", data.Service.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading "+r.plugin+" links", err.Error())
		return
	}
	if res.ExitCode != 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	found := false
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) == data.App.ValueString() {
			found = true
			break
		}
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(data.Service.ValueString() + ":" + data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serviceLinkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All attributes are RequiresReplace.
	var data ServiceLinkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *serviceLinkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ServiceLinkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked(r.plugin+":unlink", data.Service.ValueString(), data.App.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error unlinking "+r.plugin+" service", err.Error())
	}
}
