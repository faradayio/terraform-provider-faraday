package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestEveryRequiredSecretIsAnOptionalSensitiveAttribute(t *testing.T) {
	// A path that does not match the schema would panic during a plan, and a
	// Required one would make every import unplannable.
	checked := 0
	for _, def := range resourceDefs {
		s := def.schemaFn(context.Background())
		for _, secret := range resourcePerms[def.name].requiredAtCreate {
			attributes := s.Attributes
			var found schema.Attribute
			for i, name := range secret {
				a, ok := attributes[name]
				if !ok {
					t.Fatalf("%s %v: no attribute %q", def.name, []string(secret), name)
				}
				if i == len(secret)-1 {
					found = a
					break
				}
				nested, ok := a.(schema.SingleNestedAttribute)
				if !ok {
					t.Fatalf("%s %v: %q is not an object", def.name, []string(secret), name)
				}
				attributes = nested.Attributes
			}
			if !found.IsSensitive() || !found.IsOptional() || found.IsRequired() {
				t.Errorf("%s %v: want optional and sensitive", def.name, []string(secret))
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no required secrets recorded; the translator stopped finding them")
	}
}

// postgresConnection is a faraday_connection config with the postgres variant
// chosen and every member null, plus whatever members are given.
func postgresConnection(t *testing.T, s schema.Schema, members map[string]string) tftypes.Value {
	t.Helper()
	config := connectionConfigWith(t, s, "postgres").Raw
	var top map[string]tftypes.Value
	if err := config.As(&top); err != nil {
		t.Fatal(err)
	}
	var options map[string]tftypes.Value
	if err := top["options"].As(&options); err != nil {
		t.Fatal(err)
	}
	var postgres map[string]tftypes.Value
	if err := options["postgres"].As(&postgres); err != nil {
		t.Fatal(err)
	}
	for name, value := range members {
		postgres[name] = tftypes.NewValue(tftypes.String, value)
	}
	options["postgres"] = tftypes.NewValue(options["postgres"].Type(), postgres)
	top["options"] = tftypes.NewValue(top["options"].Type(), options)
	return tftypes.NewValue(config.Type(), top)
}

func planCreate(t *testing.T, s schema.Schema, config tftypes.Value, prior tftypes.Value) *resource.ModifyPlanResponse {
	t.Helper()
	r := &genericResource{def: resourceDef{name: "connection"}}
	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Raw: config, Schema: s}}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Config: tfsdk.Config{Raw: config, Schema: s},
		Plan:   tfsdk.Plan{Raw: config, Schema: s},
		State:  tfsdk.State{Raw: prior, Schema: s},
	}, resp)
	return resp
}

func TestPlannedCreateWithoutARequiredSecretFails(t *testing.T) {
	s := resourceSchema(t, "connection")
	noState := tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)

	resp := planCreate(t, s, postgresConnection(t, s, nil), noState)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a postgres connection without a password planned a create")
	}
	withPath, ok := resp.Diagnostics.Errors()[0].(diag.DiagnosticWithPath)
	if !ok {
		t.Fatal("the error does not point at an attribute")
	}
	if want := path.Root("options").AtName("postgres").AtName("password"); !withPath.Path().Equal(want) {
		t.Errorf("error at %s, want %s", withPath.Path(), want)
	}
}

func TestPlannedCreateWithTheSecretPasses(t *testing.T) {
	s := resourceSchema(t, "connection")
	noState := tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)

	resp := planCreate(t, s, postgresConnection(t, s, map[string]string{"password": "hunter2"}), noState)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a postgres connection with a password was refused: %v", resp.Diagnostics)
	}
}

func TestSecretsOfAnUnchosenVariantAreNotRequired(t *testing.T) {
	s := resourceSchema(t, "connection")
	noState := tftypes.NewValue(s.Type().TerraformType(context.Background()), nil)

	resp := planCreate(t, s, connectionConfigWith(t, s, "hosted_csv").Raw, noState)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a hosted_csv connection was asked for another variant's secret: %v", resp.Diagnostics)
	}
}

func TestAnExistingResourceIsNotAskedForItsSecret(t *testing.T) {
	// An imported connection never has its password: the API does not return it.
	s := resourceSchema(t, "connection")
	config := postgresConnection(t, s, nil)

	resp := planCreate(t, s, config, config)
	if resp.Diagnostics.HasError() {
		t.Fatalf("an existing connection was asked for its password: %v", resp.Diagnostics)
	}
}
