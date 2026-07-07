package provider

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource                = &AppScaleResource{}
	_ resource.ResourceWithConfigure   = &AppScaleResource{}
	_ resource.ResourceWithImportState = &AppScaleResource{}
)

func NewAppScaleResource() resource.Resource { return &AppScaleResource{} }

// AppScaleResource models the process scaling of a Dokku app
// (`dokku ps:scale`), keyed by process type (e.g. web, worker) with the
// desired replica count.
type AppScaleResource struct {
	client *dokku.Client
}

type AppScaleResourceModel struct {
	App   types.String `tfsdk:"app"`
	Scale types.Map    `tfsdk:"scale"`
	ID    types.String `tfsdk:"id"`
}

func (r *AppScaleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_scale"
}

func (r *AppScaleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages process scaling on a Dokku app (`dokku ps:scale`).",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"scale": schema.MapAttribute{
				Required:    true,
				ElementType: types.Int64Type,
				Description: "Map of process type (e.g. web, worker) to desired replica count.",
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

func (r *AppScaleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *AppScaleResource) set(app string, scale map[string]int64) error {
	if len(scale) == 0 {
		return nil
	}

	keys := make([]string, 0, len(scale))
	for k := range scale {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := []string{"ps:scale", app}
	for _, k := range keys {
		args = append(args, k+"="+strconv.FormatInt(scale[k], 10))
	}
	_, err := r.client.RunChecked(args...)
	return err
}

// formations reports the current replica count for every process type Dokku
// knows about for app, keyed by process type.
func (r *AppScaleResource) formations(app string) (map[string]int64, error) {
	res, err := r.client.RunChecked("ps:scale", app, "--format", "json")
	if err != nil {
		return nil, err
	}

	var parsed []struct {
		ProcessType string `json:"process_type"`
		Quantity    int64  `json:"quantity"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		return nil, err
	}

	out := make(map[string]int64, len(parsed))
	for _, f := range parsed {
		out[f.ProcessType] = f.Quantity
	}
	return out, nil
}

func (r *AppScaleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppScaleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	scale := int64MapToGo(data.Scale)

	if err := r.set(app, scale); err != nil {
		resp.Diagnostics.AddError("Error setting app scale", err.Error())
		return
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppScaleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppScaleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	scale := int64MapToGo(data.Scale)

	current, err := r.formations(app)
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	updated := make(map[string]int64, len(scale))
	for key := range scale {
		if quantity, ok := current[key]; ok {
			updated[key] = quantity
		}
	}

	scaleValue, diags := types.MapValueFrom(ctx, types.Int64Type, updated)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Scale = scaleValue
	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppScaleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppScaleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppScaleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()
	planScale := int64MapToGo(plan.Scale)
	stateScale := int64MapToGo(state.Scale)

	toSet := make(map[string]int64)
	for key, quantity := range planScale {
		if old, ok := stateScale[key]; !ok || old != quantity {
			toSet[key] = quantity
		}
	}
	// Dokku has no "unset" for a process type; scaling it to zero is the
	// closest equivalent to removing it from the managed map.
	for key := range stateScale {
		if _, ok := planScale[key]; !ok {
			toSet[key] = 0
		}
	}

	if err := r.set(app, toSet); err != nil {
		resp.Diagnostics.AddError("Error updating app scale", err.Error())
		return
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppScaleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppScaleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	scale := int64MapToGo(data.Scale)
	toZero := make(map[string]int64, len(scale))
	for key := range scale {
		toZero[key] = 0
	}

	if err := r.set(data.App.ValueString(), toZero); err != nil {
		resp.Diagnostics.AddError("Error resetting app scale", err.Error())
	}
}

func (r *AppScaleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
