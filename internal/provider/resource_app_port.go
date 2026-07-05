package provider

import (
	"context"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &AppPortResource{}
	_ resource.ResourceWithConfigure = &AppPortResource{}
)

func NewAppPortResource() resource.Resource { return &AppPortResource{} }

// AppPortResource models a single port mapping on a Dokku app
// (`dokku ports:add` / `dokku ports:remove`).
type AppPortResource struct {
	client *dokku.Client
}

type AppPortResourceModel struct {
	App           types.String `tfsdk:"app"`
	Scheme        types.String `tfsdk:"scheme"`
	HostPort      types.Int64  `tfsdk:"host_port"`
	ContainerPort types.Int64  `tfsdk:"container_port"`
	ID            types.String `tfsdk:"id"`
}

func (r *AppPortResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_port"
}

func (r *AppPortResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a single port mapping on a Dokku app (`dokku ports:add`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scheme": schema.StringAttribute{
				Required:    true,
				Description: "Proxy scheme, e.g. http, https, tcp.",
			},
			"host_port": schema.Int64Attribute{
				Required:    true,
				Description: "Host-facing port.",
			},
			"container_port": schema.Int64Attribute{
				Required:    true,
				Description: "Container-facing port.",
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

func (r *AppPortResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func portSpec(data *AppPortResourceModel) string {
	return data.Scheme.ValueString() + ":" +
		strconv.FormatInt(data.HostPort.ValueInt64(), 10) + ":" +
		strconv.FormatInt(data.ContainerPort.ValueInt64(), 10)
}

func (r *AppPortResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppPortResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	spec := portSpec(&data)
	if _, err := r.client.RunChecked("ports:add", data.App.ValueString(), spec); err != nil {
		resp.Diagnostics.AddError("Error adding port mapping", err.Error())
		return
	}

	data.ID = types.StringValue(data.App.ValueString() + ":" + spec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppPortResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppPortResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report("ports", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	spec := portSpec(&data)
	if !strings.Contains(report["map"], spec) {
		resp.State.RemoveResource(ctx)
		return
	}

	data.ID = types.StringValue(data.App.ValueString() + ":" + spec)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppPortResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppPortResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppPortResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	oldSpec := portSpec(&state)
	newSpec := portSpec(&plan)
	if oldSpec != newSpec {
		if _, err := r.client.RunChecked("ports:remove", state.App.ValueString(), oldSpec); err != nil {
			resp.Diagnostics.AddError("Error removing old port mapping", err.Error())
			return
		}
		if _, err := r.client.RunChecked("ports:add", plan.App.ValueString(), newSpec); err != nil {
			resp.Diagnostics.AddError("Error adding new port mapping", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppPortResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppPortResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	spec := portSpec(&data)
	if _, err := r.client.RunChecked("ports:remove", data.App.ValueString(), spec); err != nil {
		resp.Diagnostics.AddError("Error removing port mapping", err.Error())
	}
}
