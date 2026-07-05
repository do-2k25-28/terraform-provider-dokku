// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &StorageAssignmentResource{}
	_ resource.ResourceWithConfigure = &StorageAssignmentResource{}
)

func NewStorageAssignmentResource() resource.Resource { return &StorageAssignmentResource{} }

// StorageAssignmentResource models mounting a named Dokku storage entry
// (a persistent volume) onto an app's container filesystem, i.e. a
// "storage assignation" in Dokku parlance (`dokku storage:mount`).
type StorageAssignmentResource struct {
	client *dokku.Client
}

type StorageAssignmentResourceModel struct {
	App         types.String `tfsdk:"app"`
	StorageName types.String `tfsdk:"storage_name"`
	Destination types.String `tfsdk:"destination"`
	HostPath    types.String `tfsdk:"host_path"`
	ID          types.String `tfsdk:"id"`
}

func (r *StorageAssignmentResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_assignment"
}

func (r *StorageAssignmentResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Assigns (mounts) a dokku_storage entry onto an app (`dokku storage:mount`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required:    true,
				Description: "App to mount the storage entry onto.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"storage_name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the dokku_storage entry to mount.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination": schema.StringAttribute{
				Required:    true,
				Description: "Container path the storage entry is mounted at.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"host_path": schema.StringAttribute{
				Computed:    true,
				Description: "Resolved host path backing storage_name.",
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

func (r *StorageAssignmentResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *StorageAssignmentResource) hostPath(name string) (string, error) {
	res, err := r.client.RunChecked("storage:info", name, "--format", "json")
	if err != nil {
		return "", err
	}
	hp := parseJSONField(res.Stdout, "host_path")
	if hp == "" {
		return "", fmt.Errorf("could not resolve host_path for storage entry %q", name)
	}
	return hp, nil
}

func mountSpec(hostPath, destination string) string {
	return hostPath + ":" + destination
}

func (r *StorageAssignmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data StorageAssignmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hp, err := r.hostPath(data.StorageName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error resolving storage entry", err.Error())
		return
	}

	spec := mountSpec(hp, data.Destination.ValueString())
	if _, err := r.client.RunChecked("storage:mount", data.App.ValueString(), spec); err != nil {
		resp.Diagnostics.AddError("Error mounting storage entry", err.Error())
		return
	}

	data.HostPath = types.StringValue(hp)
	data.ID = types.StringValue(data.App.ValueString() + ":" + spec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageAssignmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data StorageAssignmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	res, err := r.client.Run("storage:list", data.App.ValueString(), "--format", "json")
	if err != nil {
		resp.Diagnostics.AddError("Error reading storage mounts", err.Error())
		return
	}
	if res.ExitCode != 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	hp, err := r.hostPath(data.StorageName.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	mounts, err := parseJSONList(res.Stdout)
	if err != nil {
		resp.Diagnostics.AddError("Error parsing storage mounts", err.Error())
		return
	}
	found := false
	for _, m := range mounts {
		if m["host_path"] == hp && m["container_path"] == data.Destination.ValueString() {
			found = true
			break
		}
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	spec := mountSpec(hp, data.Destination.ValueString())

	data.HostPath = types.StringValue(hp)
	data.ID = types.StringValue(data.App.ValueString() + ":" + spec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageAssignmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All attributes are RequiresReplace.
	var data StorageAssignmentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *StorageAssignmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data StorageAssignmentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hp := data.HostPath.ValueString()
	if hp == "" {
		var err error
		hp, err = r.hostPath(data.StorageName.ValueString())
		if err != nil {
			// Storage entry already gone; nothing left to unmount.
			return
		}
	}
	spec := mountSpec(hp, data.Destination.ValueString())

	if _, err := r.client.RunChecked("storage:unmount", data.App.ValueString(), spec); err != nil {
		resp.Diagnostics.AddError("Error unmounting storage entry", err.Error())
	}
}
