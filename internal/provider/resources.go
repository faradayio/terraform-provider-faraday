package provider

// One resource implementation for every Faraday resource type.
//
// The schema comes from api.interfaces.yml and the marshalling is generic
// (marshal.go), so nothing per-resource remains except an API path and a
// Terraform type name. That is a table, not fifteen near-identical files —
// and adding a sixteenth resource is one line plus a name in the generate step.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/faradayio/terraform-provider-faraday/internal/schemas"
)

type resourceDef struct {
	// name is the Terraform type suffix: "cohort" -> faraday_cohort.
	name string
	// collection is the REST collection path, e.g. "/cohorts".
	collection string
	schemaFn   func(context.Context) schema.Schema
	// createPath overrides where a create POSTs. Only streams needs this: it
	// creates at POST /streams/{stream_name} rather than on the collection.
	createPath func(plan tftypes.Value) (string, error)
	// providedByFaraday marks listed objects that belong to Faraday's catalog
	// rather than the account. Only traits need it: GET /traits returns the
	// catalog alongside the account's own traits.
	providedByFaraday func(apiObject map[string]any) bool
}

// isArchived reports whether the API returned an archived object. Delete
// archives, so archived objects count as absent everywhere in this provider.
func isArchived(apiObject map[string]any) bool {
	v, ok := apiObject["archived_at"]
	return ok && v != nil
}

// resourceDefs is every resource type in an account's config graph. The names
// are the public API names.
//
// `attributes` and `feature_stores` are deliberately out of scope. They are
// also the only two resource types with no /archive endpoint, which is why
// every resource here can be archived rather than destroyed (see Delete). If
// this provider ever graduates into something customers use to manage Faraday
// as code, they would need to come back -- and that missing endpoint becomes a
// real gap rather than a happy coincidence.
var resourceDefs = []resourceDef{
	{name: "atlas", collection: "/atlases", schemaFn: schemas.AtlasResourceSchema},
	{name: "cohort", collection: "/cohorts", schemaFn: schemas.CohortResourceSchema},
	{name: "connection", collection: "/connections", schemaFn: schemas.ConnectionResourceSchema},
	{name: "dataset", collection: "/datasets", schemaFn: schemas.DatasetResourceSchema},
	{
		name:       "market_opportunity_analysis",
		collection: "/market_opportunity_analyses",
		schemaFn:   schemas.MarketOpportunityAnalysisResourceSchema,
	},
	{name: "outcome", collection: "/outcomes", schemaFn: schemas.OutcomeResourceSchema},
	{name: "persona_set", collection: "/persona_sets", schemaFn: schemas.PersonaSetResourceSchema},
	{name: "place", collection: "/places", schemaFn: schemas.PlaceResourceSchema},
	{name: "recommender", collection: "/recommenders", schemaFn: schemas.RecommenderResourceSchema},
	{name: "scope", collection: "/scopes", schemaFn: schemas.ScopeResourceSchema},
	{
		name:       "stream",
		collection: "/streams",
		schemaFn:   schemas.StreamResourceSchema,
		// The one irregular resource in the API: streams are created by name on
		// the item path, not by POSTing the collection.
		createPath: func(plan tftypes.Value) (string, error) {
			name, err := stringAttr(plan, "name")
			if err != nil {
				return "", err
			}
			if name == "" {
				return "", fmt.Errorf("stream name is required to create a stream")
			}
			return "/streams/" + name, nil
		},
	},
	{name: "target", collection: "/targets", schemaFn: schemas.TargetResourceSchema},
	{
		name:       "trait",
		collection: "/traits",
		schemaFn:   schemas.TraitResourceSchema,
		providedByFaraday: func(apiObject map[string]any) bool {
			name, _ := apiObject["name"].(string)
			return strings.HasPrefix(name, "fig/")
		},
	},
}

func allResources() []func() resource.Resource {
	out := make([]func() resource.Resource, 0, len(resourceDefs))
	for _, def := range resourceDefs {
		out = append(out, func() resource.Resource { return &genericResource{def: def} })
	}
	return out
}

type genericResource struct {
	def    resourceDef
	client *client
}

func (r *genericResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.def.name
}

// Schema is fetched per call rather than cached on the struct: the framework
// does not guarantee Schema() and Create() run on the same instance, and a
// cached zero value silently produces an empty request body.
func (r *genericResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = r.def.schemaFn(ctx)
}

func (r *genericResource) IdentitySchema(_ context.Context, _ resource.IdentitySchemaRequest, resp *resource.IdentitySchemaResponse) {
	resp.IdentitySchema = identityschema.Schema{
		Attributes: map[string]identityschema.Attribute{
			"id": identityschema.StringAttribute{RequiredForImport: true},
		},
	}
}

type faradayResourceIdentity struct {
	ID types.String `tfsdk:"id"`
}

func assignIdentity(ctx context.Context, identity *tfsdk.ResourceIdentity, raw tftypes.Value, diags *diag.Diagnostics) {
	if identity == nil {
		return
	}
	id, err := stringAttr(raw, "id")
	if err != nil {
		diags.AddError("reading id", err.Error())
		return
	}
	diags.Append(identity.Set(ctx, faradayResourceIdentity{ID: types.StringValue(id)})...)
}

func (r *genericResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*client)
}

// stringAttr reads one top-level string attribute out of a raw plan/state value.
func stringAttr(raw tftypes.Value, name string) (string, error) {
	var entries map[string]tftypes.Value
	if err := raw.As(&entries); err != nil {
		return "", err
	}
	v, ok := entries[name]
	if !ok || v.IsNull() || !v.IsKnown() {
		return "", nil
	}
	var s string
	if err := v.As(&s); err != nil {
		return "", err
	}
	return s, nil
}

func (r *genericResource) itemPath(raw tftypes.Value) (string, error) {
	id, err := stringAttr(raw, "id")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("no id in state for %s", r.def.name)
	}
	return r.def.collection + "/" + id, nil
}

// ModifyPlan refuses a planned create that leaves out a secret the API
// requires, so the mistake fails `terraform plan` rather than the POST. Only a
// create: an imported or existing resource never has its secrets in state.
func (r *genericResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	for _, secret := range resourcePerms[r.def.name].requiredAtCreate {
		if secret.isMissingFrom(req.Config.Raw) {
			resp.Diagnostics.AddAttributeError(
				secret.attributePath(),
				"Missing required secret",
				fmt.Sprintf("Faraday requires %s to create the resource. It stays optional for imports, because the API never returns it.", secret.attributePath()),
			)
		}
	}
}

func (r *genericResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	s := r.def.schemaFn(ctx)
	body, diags := writableJSON(ctx, r.def.name, forCreate, s, req.Plan.Raw)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	target := r.def.collection
	if r.def.createPath != nil {
		p, err := r.def.createPath(req.Plan.Raw)
		if err != nil {
			resp.Diagnostics.AddError("creating "+r.def.name, err.Error())
			return
		}
		target = p
	}
	res, err := r.client.do(ctx, "POST", target, body)
	if err != nil {
		resp.Diagnostics.AddError("creating "+r.def.name, err.Error())
		return
	}
	// The response conceals any credential it just accepted, so the body we
	// sent is what restores it.
	raw, diags := apiToRaw(ctx, r.def.name, s, res, body)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.State.Raw = raw
	assignIdentity(ctx, resp.Identity, raw, &resp.Diagnostics)
}

func (r *genericResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	s := r.def.schemaFn(ctx)
	p, err := r.itemPath(req.State.Raw)
	if err != nil {
		resp.Diagnostics.AddError("reading state", err.Error())
		return
	}
	res, err := r.client.do(ctx, "GET", p, nil)
	if errors.Is(err, errNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("reading "+r.def.name, err.Error())
		return
	}
	// An archived resource has to read as absent, because Delete archives (see
	// below). The API still returns it with archived_at set, and taking that at
	// face value would leave Terraform believing a resource it just destroyed
	// still exists -- an error after apply, or a diff that never converges.
	// Treating archived as gone also matches how the auditor enumerates an
	// account: find_all_unarchived_and_undeleted, archived resources excluded.
	if isArchived(res) {
		resp.State.RemoveResource(ctx)
		return
	}
	raw, diags := apiToRaw(ctx, r.def.name, s, res, priorJSON(ctx, r.def.name, s, req.State.Raw))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.State.Raw = raw
	assignIdentity(ctx, resp.Identity, raw, &resp.Diagnostics)
}

func (r *genericResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	s := r.def.schemaFn(ctx)
	p, err := r.itemPath(req.State.Raw)
	if err != nil {
		resp.Diagnostics.AddError("reading state", err.Error())
		return
	}
	body, diags := writableJSON(ctx, r.def.name, forUpdate, s, req.Plan.Raw)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	res, err := r.client.do(ctx, "PATCH", p, body)
	if err != nil {
		resp.Diagnostics.AddError("updating "+r.def.name, err.Error())
		return
	}
	raw, diags := apiToRaw(ctx, r.def.name, s, res, body)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.State.Raw = raw
	assignIdentity(ctx, resp.Identity, raw, &resp.Diagnostics)
}

// Delete archives rather than destroys.
//
// This is a deliberate safety property, not an implementation detail. The
// auditor's whole job is to notice resources no documented use case calls for,
// and the likeliest explanation for one is that the knowledgebase is
// incomplete -- not that the resource is unwanted. Archiving a resource
// somebody needed costs an unarchive; deleting one costs a rebuild. Since no
// code path here issues a hard DELETE, no bug or bad plan can destroy customer
// configuration.
//
// cascade_to_all is false on purpose. The API would happily archive every
// downstream resource in one call, but then a plan showing one resource
// disappearing would quietly archive a dozen -- state and reality diverge and
// the blast radius never appears in review. With it false, the API refuses
// when dependents remain, and Terraform's own dependency graph does the
// ordering: dependents are destroyed first, so each archive call finds nothing
// downstream and succeeds, and every affected resource shows up in the plan.
func (r *genericResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	p, err := r.itemPath(req.State.Raw)
	if err != nil {
		resp.Diagnostics.AddError("reading state", err.Error())
		return
	}
	body := map[string]any{"cascade_to_all": false}
	_, err = r.client.do(ctx, "POST", p+"/archive", body)
	if err == nil || errors.Is(err, errNotFound) {
		return
	}
	if !neverBuilt(err) {
		resp.Diagnostics.AddError("archiving "+r.def.name, err.Error())
		return
	}
	// The one case archiving cannot handle: a resource that never built has no
	// update_job_state row, and the API refuses to archive it. Deleting is the
	// only way to remove it, and it is also the safest possible deletion --
	// nothing was ever built from this resource, so there are no data products
	// behind it to lose. Without the fallback the finding would recur on every
	// audit and fail identically every time.
	//
	// Deliberately narrow: anything but this exact failure still errors out. If
	// the API's wording changes the match stops firing and destroy fails loudly
	// with the 400, which is the right way for this to break.
	if _, delErr := r.client.do(ctx, "DELETE", p, nil); delErr != nil &&
		!errors.Is(delErr, errNotFound) {
		resp.Diagnostics.AddError("deleting unbuilt "+r.def.name, delErr.Error())
		return
	}
	resp.Diagnostics.AddWarning(
		"deleted unbuilt "+r.def.name,
		fmt.Sprintf(
			"%s could not be archived because it never built, so it was deleted "+
				"instead. Nothing was built from it, so no data products were lost.",
			p,
		),
	)
}

// neverBuilt reports whether an archive failed because the resource has no
// update_job_state row. The API rejects those with a 400. Matching the message
// is unavoidable: the API returns a generic VALIDATION_FAILED code that other
// failures share, so the code alone would let unrelated validation errors
// through to a hard delete.
func neverBuilt(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(apiErr.body, "has not built yet")
}

// ImportState is what makes auditing a hand-configured account possible: the
// account was not built by Terraform, so every existing resource has to be
// pulled into state before a plan can mean anything.
func (r *genericResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughWithIdentity(ctx, path.Root("id"), path.Root("id"), req, resp)
	if resp.Diagnostics.HasError() || req.ID == "" {
		return
	}
	assignIdentity(ctx, resp.Identity, resp.State.Raw, &resp.Diagnostics)
}
