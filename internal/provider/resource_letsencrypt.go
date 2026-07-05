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
	_ resource.Resource                = &LetsencryptResource{}
	_ resource.ResourceWithConfigure   = &LetsencryptResource{}
	_ resource.ResourceWithImportState = &LetsencryptResource{}
)

func NewLetsencryptResource() resource.Resource { return &LetsencryptResource{} }

// LetsencryptResource models Let's Encrypt certificate management for a
// Dokku app via the dokku-letsencrypt plugin (`dokku letsencrypt:enable` /
// `dokku letsencrypt:disable`). Requires the letsencrypt plugin to be
// installed on the target host (`dokku plugin:install
// https://github.com/dokku/dokku-letsencrypt.git`, which itself requires
// root on the Dokku host and is out of scope for this provider).
type LetsencryptResource struct {
	client *dokku.Client
}

type LetsencryptResourceModel struct {
	App   types.String `tfsdk:"app"`
	Email types.String `tfsdk:"email"`
	ID    types.String `tfsdk:"id"`
}

func (r *LetsencryptResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_letsencrypt"
}

func (r *LetsencryptResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Enables Let's Encrypt certificate management for a Dokku app (`dokku letsencrypt:enable`). Requires the dokku-letsencrypt plugin.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required:    true,
				Description: "App to enable Let's Encrypt for.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"email": schema.StringAttribute{
				Optional:    true,
				Description: "Contact email used for ACME registration/renewal notices.",
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

func (r *LetsencryptResource) setEmail(app, email string) error {
	if email == "" {
		return nil
	}
	_, err := r.client.RunChecked("letsencrypt:set", app, "email", email)
	return err
}

func (r *LetsencryptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	if err := r.setEmail(app, data.Email.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error setting letsencrypt email", err.Error())
		return
	}

	if _, err := r.client.RunChecked("letsencrypt:enable", app); err != nil {
		resp.Diagnostics.AddError("Error enabling letsencrypt", err.Error())
		return
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LetsencryptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	report, err := r.client.Report("letsencrypt", data.App.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	if email := report["email"]; email != "" {
		data.Email = types.StringValue(email)
	} else if email := report["computed-email"]; email != "" {
		data.Email = types.StringValue(email)
	}

	data.ID = types.StringValue(data.App.ValueString())
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

	app := plan.App.ValueString()
	if plan.Email.ValueString() != state.Email.ValueString() {
		if err := r.setEmail(app, plan.Email.ValueString()); err != nil {
			resp.Diagnostics.AddError("Error updating letsencrypt email", err.Error())
			return
		}
		// Re-issue so the new contact email takes effect on the cert.
		if _, err := r.client.RunChecked("letsencrypt:enable", app); err != nil {
			resp.Diagnostics.AddError("Error re-enabling letsencrypt", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *LetsencryptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LetsencryptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.RunChecked("letsencrypt:disable", data.App.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error disabling letsencrypt", err.Error())
	}
}

func (r *LetsencryptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("app"), req, resp)
}
