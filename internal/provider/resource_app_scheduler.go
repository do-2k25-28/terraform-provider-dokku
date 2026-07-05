// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

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
	_ resource.Resource                = &AppSchedulerResource{}
	_ resource.ResourceWithConfigure   = &AppSchedulerResource{}
	_ resource.ResourceWithImportState = &AppSchedulerResource{}
)

func NewAppSchedulerResource() resource.Resource { return &AppSchedulerResource{} }

// AppSchedulerResource models the scheduler selected for a Dokku app
// (`dokku scheduler:set <app> selected <value>`).
type AppSchedulerResource struct {
	client *dokku.Client
}

type AppSchedulerResourceModel struct {
	App       types.String `tfsdk:"app"`
	Scheduler types.String `tfsdk:"scheduler"`
	ID        types.String `tfsdk:"id"`
}

func (r *AppSchedulerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_scheduler"
}

func (r *AppSchedulerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Sets the scheduler used to deploy a Dokku app (`dokku scheduler:set`), e.g. docker-local or k3s.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scheduler": schema.StringAttribute{
				Required:    true,
				Description: "Scheduler to select for this app, e.g. docker-local, k3s.",
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

func (r *AppSchedulerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppSchedulerResource) set(app, value string) error {
	_, err := r.client.RunChecked("scheduler:set", app, "selected", value)
	return err
}

func (r *AppSchedulerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppSchedulerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(data.App.ValueString(), data.Scheduler.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error setting app scheduler", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppSchedulerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppSchedulerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report("scheduler", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	data.Scheduler = types.StringValue(report["selected"])
	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppSchedulerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data AppSchedulerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.set(data.App.ValueString(), data.Scheduler.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error updating app scheduler", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppSchedulerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppSchedulerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked("scheduler:set", data.App.ValueString(), "selected"); err != nil {
		resp.Diagnostics.AddError("Error clearing app scheduler", err.Error())
	}
}

func (r *AppSchedulerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
