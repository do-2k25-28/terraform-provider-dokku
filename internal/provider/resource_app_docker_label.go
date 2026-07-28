package provider

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/dokku/terraform-provider-dokku/internal/dokku"
)

var (
	_ resource.Resource              = &AppDockerLabelResource{}
	_ resource.ResourceWithConfigure = &AppDockerLabelResource{}
)

func NewAppDockerLabelResource() resource.Resource { return &AppDockerLabelResource{} }

// dockerLabelPhase is the docker-options phase container labels are added
// under. Dokku has no dedicated labels plugin for the default docker-local
// scheduler, so labels are implemented as "--label key=value" docker-options
// (dokku docker-options:add/:remove); "deploy" is the phase that governs the
// app's actual running container.
const dockerLabelPhase = "deploy"

// AppDockerLabelResource models the set of Docker labels applied to a Dokku
// app's deployed container.
type AppDockerLabelResource struct {
	client *dokku.Client
}

type AppDockerLabelResourceModel struct {
	App    types.String `tfsdk:"app"`
	Labels types.Map    `tfsdk:"labels"`
	ID     types.String `tfsdk:"id"`
}

func (r *AppDockerLabelResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_docker_label"
}

func (r *AppDockerLabelResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the Docker labels applied to a Dokku app's deployed container, via `--label` docker-options on the deploy phase (`dokku docker-options:add`). See https://dokku.com/docs/advanced-usage/docker-options/.",
		Attributes: map[string]schema.Attribute{
			"app": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"labels": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of Docker label keys to values applied to the app's deployed container. Values may contain single spaces and shell-special characters (e.g. Traefik routing rules), which are shell-quoted for transport, but not tabs, newlines, or multiple consecutive spaces, since the SSH forced-command interface collapses those when reconstructing the command server-side.",
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

func (r *AppDockerLabelResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// needsShellQuoting reports whether s contains a character that Dokku's own
// docker-options tokenizer (mvdan.cc/sh, see SplitOptionString/quoteShellArg
// in dokku/plugins/docker-options/dockeroptions.go) would otherwise treat as
// shell syntax rather than literal text. It mirrors that package's
// needsShellQuoting exactly, since our quoting only round-trips correctly if
// it agrees with the parser on the other end.
func needsShellQuoting(s string) bool {
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '"', '\'', '$', '`', '\\',
			'*', '?', '[', ']', '<', '>', '|', '&', ';',
			'(', ')', '{', '}', '!', '#', '~':
			return true
		}
	}
	return false
}

// quoteShellArg single-quotes s if it needs shell quoting, escaping any
// embedded single quote by closing the quote, inserting a backslash-escaped
// quote, then reopening it (the standard shell close-escape-open trick).
// This is what makes a label value containing whitespace (or other shell
// metacharacters) survive as one token: the provider's own SSH transport
// re-splits/re-joins the command on plain whitespace with no quote handling
// (see dokku.joinArgs), so it round-trips a quoted value unchanged, and
// Dokku's docker-options plugin then does its own proper shell-tokenizing
// parse of the reconstructed string, honoring these quotes to keep the
// value as a single token.
func quoteShellArg(s string) string {
	if !needsShellQuoting(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unquoteShellArg reverses quoteShellArg: if s is wrapped in single quotes,
// it strips them and reverses the close-escape-open trick back into a
// literal embedded quote.
func unquoteShellArg(s string) string {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return s
	}
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
}

// parseDockerLabelOption extracts the key/value out of a raw docker-options
// entry, as reported by `docker-options:report`. The value is stored
// verbatim ("--label com.example.env=production") when it needs no quoting,
// or with the whole "key=value" spec single-quoted ("--label
// 'com.example.env=hello world'") when it does, since Dokku's docker-options
// plugin re-serializes stored options that way (see dockeroptions.go).
func parseDockerLabelOption(opt string) (key, value string, ok bool) {
	rest, found := strings.CutPrefix(opt, "--label ")
	if !found {
		return "", "", false
	}
	return strings.Cut(unquoteShellArg(rest), "=")
}

// dockerLabelOption builds the raw docker-options OPTION argument for a
// single label. The whole "--label key=value" phrase is shell-quoted as one
// unit, matching Dokku's documented docker-options:add/:remove usage where
// OPTION is a single CLI argument (e.g. `dokku docker-options:add app phase
// '--label key=value'`). Quoting only the "key=value" spec would leave
// "--label" and the value as two separate tokens instead of one option.
func dockerLabelOption(key, value string) string {
	return quoteShellArg("--label " + key + "=" + value)
}

func (r *AppDockerLabelResource) add(app string, labels map[string]string) error {
	for key, value := range labels {
		if _, err := r.client.RunChecked("docker-options:add", app, dockerLabelPhase, dockerLabelOption(key, value)); err != nil {
			return err
		}
	}
	return nil
}

func (r *AppDockerLabelResource) remove(app string, labels map[string]string) error {
	for key, value := range labels {
		if _, err := r.client.RunChecked("docker-options:remove", app, dockerLabelPhase, dockerLabelOption(key, value)); err != nil {
			return err
		}
	}
	return nil
}

// dockerLabels runs `docker-options:report <app> --format json` and returns
// the currently applied "--label key=value" entries for dockerLabelPhase as
// a map. It can't go through the shared Client.Report helper, which
// stringifies every value, since the report mixes flat string fields with
// "<phase>-list" array fields.
func (r *AppDockerLabelResource) dockerLabels(app string) (map[string]string, error) {
	res, err := r.client.RunChecked("docker-options:report", app, "--format", "json")
	if err != nil {
		return nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &raw); err != nil {
		return nil, err
	}

	labels := make(map[string]string)
	arr, ok := raw[dockerLabelPhase+"-list"].([]any)
	if !ok {
		return labels, nil
	}
	for _, e := range arr {
		opt, ok := e.(string)
		if !ok {
			continue
		}
		if key, value, ok := parseDockerLabelOption(opt); ok {
			labels[key] = value
		}
	}
	return labels, nil
}

func (r *AppDockerLabelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data AppDockerLabelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.add(app, labels); err != nil {
		resp.Diagnostics.AddError("Error adding app docker labels", err.Error())
		return
	}

	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerLabelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data AppDockerLabelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := data.App.ValueString()
	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.dockerLabels(app)
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	result := make(map[string]string, len(labels))
	for key := range labels {
		// Label was removed outside of Terraform; drop it from state so the
		// plan shows it as needing to be (re)created.
		if value, ok := current[key]; ok {
			result[key] = value
		}
	}

	labelsValue, diags := types.MapValueFrom(ctx, types.StringType, result)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.Labels = labelsValue
	data.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *AppDockerLabelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan AppDockerLabelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var state AppDockerLabelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	app := plan.App.ValueString()
	var planLabels, stateLabels map[string]string
	resp.Diagnostics.Append(plan.Labels.ElementsAs(ctx, &planLabels, false)...)
	resp.Diagnostics.Append(state.Labels.ElementsAs(ctx, &stateLabels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	toRemove := make(map[string]string)
	for key, value := range stateLabels {
		if newValue, ok := planLabels[key]; !ok || newValue != value {
			toRemove[key] = value
		}
	}

	toAdd := make(map[string]string)
	for key, value := range planLabels {
		if oldValue, ok := stateLabels[key]; !ok || oldValue != value {
			toAdd[key] = value
		}
	}

	if err := r.remove(app, toRemove); err != nil {
		resp.Diagnostics.AddError("Error removing old app docker labels", err.Error())
		return
	}
	if err := r.add(app, toAdd); err != nil {
		resp.Diagnostics.AddError("Error adding new app docker labels", err.Error())
		return
	}

	plan.ID = types.StringValue(app)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *AppDockerLabelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data AppDockerLabelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var labels map[string]string
	resp.Diagnostics.Append(data.Labels.ElementsAs(ctx, &labels, false)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.remove(data.App.ValueString(), labels); err != nil {
		resp.Diagnostics.AddError("Error removing app docker labels", err.Error())
	}
}
