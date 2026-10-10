package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/list/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ list.ListResource              = (*genericListResource)(nil)
	_ list.ListResourceWithConfigure = (*genericListResource)(nil)
)

func allListResources() []func() list.ListResource {
	out := make([]func() list.ListResource, 0, len(resourceDefs))
	for _, def := range resourceDefs {
		out = append(out, func() list.ListResource { return &genericListResource{def: def} })
	}
	return out
}

type genericListResource struct {
	def    resourceDef
	client *client
}

func (r *genericListResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.def.name
}

func (r *genericListResource) ListResourceConfigSchema(_ context.Context, _ list.ListResourceSchemaRequest, resp *list.ListResourceSchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{}}
}

func (r *genericListResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.client = req.ProviderData.(*client)
}

func (r *genericListResource) List(ctx context.Context, req list.ListRequest, stream *list.ListResultsStream) {
	items, err := r.client.list(ctx, r.def.collection)
	if err != nil {
		stream.Results = func(push func(list.ListResult) bool) {
			push(list.ListResult{
				Diagnostics: diag.Diagnostics{
					diag.NewErrorDiagnostic("listing "+r.def.name, err.Error()),
				},
			})
		}
		return
	}
	resourceSchema := r.def.schemaFn(ctx)
	stream.Results = func(push func(list.ListResult) bool) {
		emitted := int64(0)
		for _, item := range items {
			if isArchived(item) || r.providedByFaraday(item) {
				continue
			}
			if req.Limit > 0 && emitted >= req.Limit {
				return
			}
			id, ok := item["id"].(string)
			if !ok || id == "" {
				if !push(list.ListResult{
					Diagnostics: diag.Diagnostics{
						diag.NewErrorDiagnostic(
							"listing "+r.def.name,
							r.def.collection+" returned an object with no id",
						),
					},
				}) {
					return
				}
				continue
			}
			result := req.NewListResult(ctx)
			result.DisplayName = id
			if name, ok := item["name"].(string); ok && name != "" {
				result.DisplayName = name
			}
			result.Diagnostics.Append(result.Identity.Set(ctx, faradayResourceIdentity{ID: types.StringValue(id)})...)
			if req.IncludeResource {
				raw, diags := apiToRaw(ctx, r.def.name, resourceSchema, item, nil)
				result.Diagnostics.Append(diags...)
				if !diags.HasError() {
					result.Resource.Raw = raw
				}
			}
			if !push(result) {
				return
			}
			emitted++
		}
	}
}

func (r *genericListResource) providedByFaraday(apiObject map[string]any) bool {
	return r.def.providedByFaraday != nil && r.def.providedByFaraday(apiObject)
}
