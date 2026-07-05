// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strconv"
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
	_ resource.Resource                = &AppNetworkResource{}
	_ resource.ResourceWithConfigure   = &AppNetworkResource{}
	_ resource.ResourceWithImportState = &AppNetworkResource{}
)

func NewAppNetworkResource() resource.Resource { return &AppNetworkResource{} }

// AppNetworkResource models the network properties of a Dokku app
// (`dokku network:set <app> <property> <value...>`), attaching it to one or
// more Docker networks at various points in the container lifecycle.
type AppNetworkResource struct {
	client *dokku.Client
}

type AppNetworkResourceModel struct {
	App               types.String `tfsdk:"app"`
	AttachPostCreate  types.List   `tfsdk:"attach_post_create"`
	AttachPostDeploy  types.List   `tfsdk:"attach_post_deploy"`
	InitialNetwork    types.String `tfsdk:"initial_network"`
	BindAllInterfaces types.Bool   `tfsdk:"bind_all_interfaces"`
	ID                types.String `tfsdk:"id"`
}

func (r *AppNetworkResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_network"
}

func (r *AppNetworkResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the network properties of a Dokku app (`dokku network:set`), attaching it to one or more Docker networks. " +
			"Changing attach_post_create, attach_post_deploy, or initial_network requires an app deploy or rebuild to actually move a running container.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"attach_post_create": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Networks to attach after the container is created but before it is started. Commonly used for cross-app networking.",
			},
			"attach_post_deploy": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Networks to attach after a successful deploy but before the proxy is updated. Used when healthchecks must be invoked first.",
			},
			"initial_network": schema.StringAttribute{
				Optional:    true,
				Description: "Network to attach at container creation. Typically blocks access to services and external routing.",
			},
			"bind_all_interfaces": schema.BoolAttribute{
				Optional:    true,
				Description: "Whether the app binds to all network interfaces (0.0.0.0) instead of just the internal interface.",
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

func (r *AppNetworkResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppNetworkResource) setListProperty(app, property string, values []string) error {
	args := append([]string{"network:set", app, property}, values...)
	_, err := r.client.RunChecked(args...)
	return err
}

func (r *AppNetworkResource) setStringProperty(app, property, value string) error {
	if value == "" {
		_, err := r.client.RunChecked("network:set", app, property)
		return err
	}
	_, err := r.client.RunChecked("network:set", app, property, value)
	return err
}

func stringListToGo(ctx context.Context, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	for _, e := range l.Elements() {
		if s, ok := e.(types.String); ok {
			out = append(out, s.ValueString())
		}
	}
	return out
}

func (r *AppNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppNetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()

	if !data.AttachPostCreate.IsNull() {
		if err := r.setListProperty(app, "attach-post-create", stringListToGo(ctx, data.AttachPostCreate)); err != nil {
			resp.Diagnostics.AddError("Error setting attach_post_create", err.Error())
			return
		}
	}
	if !data.AttachPostDeploy.IsNull() {
		if err := r.setListProperty(app, "attach-post-deploy", stringListToGo(ctx, data.AttachPostDeploy)); err != nil {
			resp.Diagnostics.AddError("Error setting attach_post_deploy", err.Error())
			return
		}
	}
	if !data.InitialNetwork.IsNull() {
		if err := r.setStringProperty(app, "initial-network", data.InitialNetwork.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error setting initial_network", err.Error())
			return
		}
	}
	if !data.BindAllInterfaces.IsNull() {
		if err := r.setStringProperty(app, "bind-all-interfaces", strconv.FormatBool(data.BindAllInterfaces.ValueBool())); err != nil {
			resp.Diagnostics.AddError("Error setting bind_all_interfaces", err.Error())
			return
		}
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppNetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	report, err := r.client.Report("network", app)
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.AttachPostCreate = fieldsToList(report["attach-post-create"], data.AttachPostCreate)
	data.AttachPostDeploy = fieldsToList(report["attach-post-deploy"], data.AttachPostDeploy)

	if v, ok := report["initial-network"]; ok && v != "" {
		data.InitialNetwork = types.StringValue(v)
	} else if !data.InitialNetwork.IsNull() {
		data.InitialNetwork = types.StringNull()
	}

	if v, ok := report["bind-all-interfaces"]; ok && v != "" {
		data.BindAllInterfaces = types.BoolValue(v == "true")
	} else if !data.BindAllInterfaces.IsNull() {
		data.BindAllInterfaces = types.BoolNull()
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fieldsToList converts a space-separated report field into a types.List,
// preserving null when the resource was never configured with this
// attribute and the report reflects an empty/default value.
func fieldsToList(v string, prior types.List) types.List {
	if v == "" {
		if prior.IsNull() {
			return prior
		}
		return types.ListNull(types.StringType)
	}
	fields := strings.Fields(v)
	l, _ := types.ListValueFrom(context.Background(), types.StringType, fields)
	return l
}

func (r *AppNetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppNetworkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppNetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()

	planCreate := stringListToGo(ctx, plan.AttachPostCreate)
	stateCreate := stringListToGo(ctx, state.AttachPostCreate)
	if !equalStringSlices(planCreate, stateCreate) {
		if err := r.setListProperty(app, "attach-post-create", planCreate); err != nil {
			resp.Diagnostics.AddError("Error updating attach_post_create", err.Error())
			return
		}
	}

	planDeploy := stringListToGo(ctx, plan.AttachPostDeploy)
	stateDeploy := stringListToGo(ctx, state.AttachPostDeploy)
	if !equalStringSlices(planDeploy, stateDeploy) {
		if err := r.setListProperty(app, "attach-post-deploy", planDeploy); err != nil {
			resp.Diagnostics.AddError("Error updating attach_post_deploy", err.Error())
			return
		}
	}

	if plan.InitialNetwork.ValueString() != state.InitialNetwork.ValueString() {
		if err := r.setStringProperty(app, "initial-network", plan.InitialNetwork.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error updating initial_network", err.Error())
			return
		}
	}

	planBind := ""
	if !plan.BindAllInterfaces.IsNull() {
		planBind = strconv.FormatBool(plan.BindAllInterfaces.ValueBool())
	}
	stateBind := ""
	if !state.BindAllInterfaces.IsNull() {
		stateBind = strconv.FormatBool(state.BindAllInterfaces.ValueBool())
	}
	if planBind != stateBind {
		if err := r.setStringProperty(app, "bind-all-interfaces", planBind); err != nil {
			resp.Diagnostics.AddError("Error updating bind_all_interfaces", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *AppNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppNetworkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()

	if !data.AttachPostCreate.IsNull() {
		if err := r.setListProperty(app, "attach-post-create", nil); err != nil {
			resp.Diagnostics.AddError("Error clearing attach_post_create", err.Error())
		}
	}
	if !data.AttachPostDeploy.IsNull() {
		if err := r.setListProperty(app, "attach-post-deploy", nil); err != nil {
			resp.Diagnostics.AddError("Error clearing attach_post_deploy", err.Error())
		}
	}
	if !data.InitialNetwork.IsNull() {
		if err := r.setStringProperty(app, "initial-network", ""); err != nil {
			resp.Diagnostics.AddError("Error clearing initial_network", err.Error())
		}
	}
	if !data.BindAllInterfaces.IsNull() {
		if err := r.setStringProperty(app, "bind-all-interfaces", ""); err != nil {
			resp.Diagnostics.AddError("Error clearing bind_all_interfaces", err.Error())
		}
	}
}

func (r *AppNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
