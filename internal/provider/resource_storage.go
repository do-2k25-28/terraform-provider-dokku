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
	_ resource.Resource                = &StorageResource{}
	_ resource.ResourceWithConfigure   = &StorageResource{}
	_ resource.ResourceWithImportState = &StorageResource{}
)

func NewStorageResource() resource.Resource { return &StorageResource{} }

type StorageResource struct {
	client *dokku.Client
}

type StorageResourceModel struct {
	Name     types.String `tfsdk:"name"`
	Path     types.String `tfsdk:"path"`
	HostPath types.String `tfsdk:"host_path"`
	ID       types.String `tfsdk:"id"`
}

func (r *StorageResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage"
}

func (r *StorageResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a named Dokku persistent storage entry (`dokku storage:create`).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the storage entry.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"path": schema.StringAttribute{
				Optional:    true,
				Description: "Host path to back the storage entry. Defaults to Dokku's standard storage location if unset.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"host_path": schema.StringAttribute{
				Computed:    true,
				Description: "Resolved host path for this storage entry.",
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

func (r *StorageResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *StorageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data StorageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	args := []string{"storage:create", data.Name.ValueString()}
	if !data.Path.IsNull() && data.Path.ValueString() != "" {
		args = append(args, data.Path.ValueString())
	}

	if _, err := r.client.RunChecked(args...); err != nil {
		resp.Diagnostics.AddError("Error creating storage entry", err.Error())
		return
	}

	info, err := r.client.Run("storage:info", data.Name.ValueString(), "--format", "json")
	if err == nil && info.ExitCode == 0 {
		data.HostPath = types.StringValue(parseJSONField(info.Stdout, "host_path"))
	}
	data.ID = types.StringValue(data.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data StorageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.client.Run("storage:info", data.Name.ValueString(), "--format", "json")
	if err != nil {
		resp.Diagnostics.AddError("Error reading storage entry", err.Error())
		return
	}
	if res.ExitCode != 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	data.HostPath = types.StringValue(parseJSONField(res.Stdout, "host_path"))
	data.ID = types.StringValue(data.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Both name and path are RequiresReplace.
	var data StorageResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data StorageResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked("storage:destroy", data.Name.ValueString(), "--force"); err != nil {
		resp.Diagnostics.AddError("Error destroying storage entry", err.Error())
	}
}

func (r *StorageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
}
