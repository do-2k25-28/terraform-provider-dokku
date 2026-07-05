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
	_ resource.Resource                = &AppResource{}
	_ resource.ResourceWithConfigure   = &AppResource{}
	_ resource.ResourceWithImportState = &AppResource{}
)

func NewAppResource() resource.Resource { return &AppResource{} }

// AppResource models a Dokku application deployed from a container registry
// image (`dokku apps:create` + `dokku git:from-image`), optionally
// authenticating to a private registry first (`dokku registry:login`).
type AppResource struct {
	client *dokku.Client
}

type AppResourceModel struct {
	Name             types.String `tfsdk:"name"`
	Image            types.String `tfsdk:"image"`
	RegistryServer   types.String `tfsdk:"registry_server"`
	RegistryUsername types.String `tfsdk:"registry_username"`
	RegistryPassword types.String `tfsdk:"registry_password"`
	DeployedSHA      types.String `tfsdk:"deployed_sha"`
	ID               types.String `tfsdk:"id"`
}

func (r *AppResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *AppResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Dokku app deployed from a registry image (`dokku apps:create` + `dokku git:from-image`).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "App name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image": schema.StringAttribute{
				Required:    true,
				Description: "Full container image reference to deploy, e.g. registry.example.com/org/app:tag.",
			},
			"registry_server": schema.StringAttribute{
				Optional:    true,
				Description: "Registry server to authenticate against before deploying, e.g. registry.example.com. Omit for Docker Hub public images.",
			},
			"registry_username": schema.StringAttribute{
				Optional:    true,
				Description: "Username for registry authentication.",
			},
			"registry_password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Password or token for registry authentication.",
			},
			"deployed_sha": schema.StringAttribute{
				Computed:    true,
				Description: "Deploy revision reported by Dokku for the current deploy.",
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

func (r *AppResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppResource) login(name string, data *AppResourceModel) error {
	if data.RegistryServer.ValueString() == "" {
		return nil
	}
	_, err := r.client.RunChecked(
		"registry:login",
		name,
		data.RegistryServer.ValueString(),
		data.RegistryUsername.ValueString(),
		data.RegistryPassword.ValueString(),
	)
	return err
}

func (r *AppResource) deploy(name string, data *AppResourceModel) error {
	_, err := r.client.RunChecked("git:from-image", name, data.Image.ValueString())
	return err
}

func (r *AppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	if _, err := r.client.RunChecked("apps:create", name); err != nil {
		resp.Diagnostics.AddError("Error creating app", err.Error())
		return
	}

	if err := r.login(name, &data); err != nil {
		resp.Diagnostics.AddError("Error authenticating to registry", err.Error())
		return
	}

	if err := r.deploy(name, &data); err != nil {
		resp.Diagnostics.AddError("Error deploying app image", err.Error())
		return
	}

	data.ID = types.StringValue(name)
	r.populateComputed(&data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppResource) populateComputed(data *AppResourceModel) {
	report, err := r.client.Report("git", data.Name.ValueString())
	if err != nil {
		data.DeployedSHA = types.StringValue("")
		return
	}
	data.DeployedSHA = types.StringValue(report["sha"])
}

func (r *AppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := data.Name.ValueString()
	if _, err := r.client.Report("apps", name); err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	gitReport, err := r.client.Report("git", name)
	if err == nil {
		data.DeployedSHA = types.StringValue(gitReport["sha"])
		if img := gitReport["source-image"]; img != "" {
			data.Image = types.StringValue(img)
		}
	}

	data.ID = types.StringValue(name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()

	credsChanged := plan.RegistryServer.ValueString() != state.RegistryServer.ValueString() ||
		plan.RegistryUsername.ValueString() != state.RegistryUsername.ValueString() ||
		plan.RegistryPassword.ValueString() != state.RegistryPassword.ValueString()

	if credsChanged {
		if plan.RegistryServer.ValueString() == "" && state.RegistryServer.ValueString() != "" {
			if _, err := r.client.RunChecked("registry:logout", name, state.RegistryServer.ValueString()); err != nil {
				resp.Diagnostics.AddError("Error logging out of registry", err.Error())
				return
			}
		} else if err := r.login(name, &plan); err != nil {
			resp.Diagnostics.AddError("Error authenticating to registry", err.Error())
			return
		}
	}

	if plan.Image.ValueString() != state.Image.ValueString() {
		if err := r.deploy(name, &plan); err != nil {
			resp.Diagnostics.AddError("Error redeploying app image", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(name)
	r.populateComputed(&plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked("apps:destroy", data.Name.ValueString(), "--force"); err != nil {
		resp.Diagnostics.AddError("Error destroying app", err.Error())
	}
}

func (r *AppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
