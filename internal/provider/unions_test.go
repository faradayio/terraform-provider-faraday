package provider

// Terraform has no union type, so each discriminated-union variant becomes its
// own optional nested attribute and the tag is dropped -- the attribute name
// already says which variant it is. Two things have to hold for that to be
// safe, and neither is visible in a schema that merely compiles:
//
//  1. exactly one variant may be set, even though every variant is Optional;
//  2. a variant carrying nothing but its tag survives as an empty object
//     rather than vanishing.
//
// Both are pinned here.

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// knownNullObject is an object that is present but whose every attribute is
// null -- what `hosted_csv = {}` means on the wire. Distinct from a null
// object, which means the variant was not set at all; that distinction is the
// whole subject of these tests.
func knownNullObject(t tftypes.Object) tftypes.Value {
	attrs := make(map[string]tftypes.Value, len(t.AttributeTypes))
	for name, at := range t.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tftypes.NewValue(t, attrs)
}

// connectionConfigWith builds a faraday_connection config with exactly the
// named option variants set, every other attribute null.
func connectionConfigWith(
	t *testing.T, s schema.Schema, variants ...string,
) tfsdk.Config {
	t.Helper()
	ctx := context.Background()
	resourceType := s.Type().TerraformType(ctx).(tftypes.Object)
	optionsType := resourceType.AttributeTypes["options"].(tftypes.Object)

	options := make(map[string]tftypes.Value, len(optionsType.AttributeTypes))
	for name, at := range optionsType.AttributeTypes {
		options[name] = tftypes.NewValue(at, nil)
	}
	for _, v := range variants {
		at, ok := optionsType.AttributeTypes[v]
		if !ok {
			t.Fatalf("no option variant %q", v)
		}
		options[v] = knownNullObject(at.(tftypes.Object))
	}

	top := make(map[string]tftypes.Value, len(resourceType.AttributeTypes))
	for name, at := range resourceType.AttributeTypes {
		top[name] = tftypes.NewValue(at, nil)
	}
	top["options"] = tftypes.NewValue(optionsType, options)
	return tfsdk.Config{Raw: tftypes.NewValue(resourceType, top), Schema: s}
}

// validateVariants runs every validator the generator attached anywhere under
// `options` and reports whether the configuration was accepted.
//
// ExactlyOneOf is attached to ONE variant and names all the others, which is
// the idiomatic shape -- so this deliberately does not require a validator per
// variant. What it does require is that at least one exists: a schema with
// none would accept every combination and this would silently pass.
func validateVariants(t *testing.T, s schema.Schema, config tfsdk.Config) bool {
	t.Helper()
	ctx := context.Background()
	options := s.Attributes["options"].(schema.SingleNestedAttribute)

	// Read the variant values straight off the raw config. GetAttribute cannot
	// be used here: the generator gives every variant its own custom type
	// (AthenaType and friends), which does not reflect into a plain
	// types.Object.
	var top map[string]tftypes.Value
	if err := config.Raw.As(&top); err != nil {
		t.Fatalf("reading config: %v", err)
	}
	var variantValues map[string]tftypes.Value
	if err := top["options"].As(&variantValues); err != nil {
		t.Fatalf("reading options: %v", err)
	}

	accepted, ran := true, 0
	for name, a := range options.Attributes {
		nested, ok := a.(schema.SingleNestedAttribute)
		if !ok || len(nested.Validators) == 0 {
			continue
		}
		ran += len(nested.Validators)
		p := path.Root("options").AtName(name)
		typed, err := nested.GetType().ValueFromTerraform(ctx, variantValues[name])
		if err != nil {
			t.Fatalf("converting %s: %v", p, err)
		}
		value, diags := typed.(basetypes.ObjectValuable).ToObjectValue(ctx)
		if diags.HasError() {
			t.Fatalf("converting %s: %v", p, diags)
		}
		for _, v := range nested.Validators {
			resp := &validator.ObjectResponse{}
			v.ValidateObject(ctx, validator.ObjectRequest{
				Config:         config,
				Path:           p,
				PathExpression: p.Expression(),
				ConfigValue:    value,
			}, resp)
			if resp.Diagnostics.HasError() {
				accepted = false
			}
		}
	}
	if ran == 0 {
		t.Fatal("no validators anywhere under options: every combination of variants would be accepted")
	}
	return accepted
}

func TestExactlyOneUnionVariant(t *testing.T) {
	s := resourceSchema(t, "connection")

	for _, tc := range []struct {
		name     string
		variants []string
		accepted bool
	}{
		{"one variant is accepted", []string{"hosted_csv"}, true},
		{"one variant elsewhere in the set is accepted", []string{"snowflake"}, true},
		{"the tag-only variant alone is accepted", []string{"classic"}, true},
		{"no variant is rejected", nil, false},
		{"two variants are rejected", []string{"athena", "hosted_csv"}, false},
		// Neither of these is the variant carrying the validator, so a variant
		// missing from its path expressions would let this pair through.
		{"two variants away from the validator are rejected", []string{"hosted_csv", "s3_csv"}, false},
		{"two more, far apart in the set", []string{"snowflake", "tiktok"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := connectionConfigWith(t, s, tc.variants...)
			if got := validateVariants(t, s, config); got != tc.accepted {
				t.Errorf("accepted = %v, want %v", got, tc.accepted)
			}
		})
	}
}

func TestTagOnlyVariantRoundTripsAsAnEmptyObject(t *testing.T) {
	// `hosted_csv` carries nothing but its discriminator, so once the tag is
	// dropped the variant has no attributes at all. The risk is that it
	// degrades to null somewhere in the round trip and the connection type is
	// silently lost.
	ctx := context.Background()
	s := resourceSchema(t, "connection")
	resourceType := s.Type().TerraformType(ctx).(tftypes.Object)

	raw, err := jsonToTf(resourceType, map[string]any{
		"name":    "warehouse",
		"options": map[string]any{"type": "hosted_csv"},
	}, "connection")
	if err != nil {
		t.Fatalf("decoding a tag-only variant: %v", err)
	}

	// Terraform side: the variant is present, not null.
	var top map[string]tftypes.Value
	if err := raw.As(&top); err != nil {
		t.Fatal(err)
	}
	var options map[string]tftypes.Value
	if err := top["options"].As(&options); err != nil {
		t.Fatalf("options decoded to something that is not an object: %v", err)
	}
	if options["hosted_csv"].IsNull() {
		t.Error("the tag-only variant decoded to null; the connection type is lost")
	}
	for name, v := range options {
		if name != "hosted_csv" && !v.IsNull() {
			t.Errorf("variant %q should be null, got %v", name, v)
		}
	}

	// Wire side: it collapses back to exactly the tag it came from.
	body, _ := tfToJSON(top["options"])
	collapsed, err := collapseUnion(unionAttrs["connection"]["options"], body)
	if err != nil {
		t.Fatalf("collapsing the tag-only variant: %v", err)
	}
	got, ok := collapsed.(map[string]any)
	if !ok {
		t.Fatalf("collapsed to %T, want an object", collapsed)
	}
	if got["type"] != "hosted_csv" {
		t.Errorf("type = %v, want hosted_csv", got["type"])
	}
	if len(got) != 1 {
		t.Errorf("tag-only variant gained fields on the way out: %v", got)
	}
}
