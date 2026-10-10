package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/list"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/faradayio/terraform-provider-faraday/internal/resources/cohort"
)

func TestListSkipsArchivedAndHonorsLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cohorts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"live-1","name":"Customers"},
			{"id":"gone","name":"Old","archived_at":"2020-01-01T00:00:00Z"},
			{"id":"live-2","name":"Leads"}
		]`))
	}))
	defer srv.Close()

	listed := &genericListResource{
		def: resourceDef{name: "cohort", collection: "/cohorts", schemaFn: cohort.CohortResourceSchema},
		client: &client{
			apiKey:  "test-key",
			baseURL: srv.URL,
			http:    srv.Client(),
		},
	}
	req := list.ListRequest{
		IncludeResource: false,
		Limit:           1,
		ResourceSchema:  cohort.CohortResourceSchema(context.Background()),
		ResourceIdentitySchema: identityschema.Schema{
			Attributes: map[string]identityschema.Attribute{
				"id": identityschema.StringAttribute{RequiredForImport: true},
			},
		},
	}
	stream := &list.ListResultsStream{}
	listed.List(context.Background(), req, stream)

	var results []list.ListResult
	for result := range stream.Results {
		results = append(results, result)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Diagnostics.HasError() {
		t.Fatalf("diagnostics: %v", results[0].Diagnostics)
	}
	if results[0].DisplayName != "Customers" {
		t.Fatalf("display name = %q", results[0].DisplayName)
	}
	var id types.String
	results[0].Diagnostics.Append(results[0].Identity.GetAttribute(context.Background(), path.Root("id"), &id)...)
	if id.ValueString() != "live-1" {
		t.Fatalf("identity id = %q", id.ValueString())
	}
}

func TestTraitListOmitsFaradayCatalog(t *testing.T) {
	var traitDef resourceDef
	for _, def := range resourceDefs {
		if def.name == "trait" {
			traitDef = def
		}
	}
	listed := &genericListResource{def: traitDef}
	if !listed.providedByFaraday(map[string]any{"name": "fig/age"}) {
		t.Error("fig/ traits are Faraday's catalog and must not be listed")
	}
	if listed.providedByFaraday(map[string]any{"name": "lifetime_value"}) {
		t.Error("account traits must be listed")
	}
}
