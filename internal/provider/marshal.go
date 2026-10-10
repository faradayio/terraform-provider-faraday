package provider

// Generic marshalling between the Faraday API's JSON and Terraform values.
//
// The spike's first cut mapped fields by hand, and that broke the moment nested
// attributes were added to the generated schema: Terraform aborts with
// "provider returned invalid result object" for any Computed attribute left
// unknown after apply, so every new attribute had to be accounted for by hand.
// Hand-maintaining that across 15 resources would not work.
//
// Generating a marshaller per resource would work, but is unnecessary. Every
// Terraform type can describe itself as a `tftypes.Type`, and every `attr.Type`
// can rebuild a value from a `tftypes.Value` — so one pair of recursive
// functions covers every resource, including the generated custom object types,
// with no codegen at all.
//
// The direction that matters most is apiToObject: it walks the *schema*, not the
// response, so a field the API omits becomes null rather than staying unknown.
// That is exactly the failure mode above, fixed structurally.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// reservedRenames maps a Terraform attribute name to the API field it stands
// for. Terraform reserves `count` as a meta-argument and rejects any root
// attribute using it, so Target.count is exposed as target_count and translated
// back here. Keep in step with RESERVED_RENAMES in gen/interfaces_to_codespec.py.
var reservedRenames = map[string]string{"target_count": "count"}

// apiFieldName is the wire name for a Terraform attribute name.
func apiFieldName(name string) string {
	if api, ok := reservedRenames[name]; ok {
		return api
	}
	return name
}

// jsonToTf converts a decoded-JSON value into a tftypes.Value of the given type.
//
// Objects are driven by the *type's* attribute list, not the JSON's keys: every
// declared attribute gets a value (null when absent) and unknown JSON keys are
// ignored. That is what guarantees nothing is left unknown after apply.
// `resource` scopes the union and JSON-encoding lookups. Both are per
// (resource, attribute): `options` is a union on atlas, connection, dataset and
// target with a DIFFERENT variant list on each.
func jsonToTf(t tftypes.Type, v any, resource string) (tftypes.Value, error) {
	if v == nil {
		return tftypes.NewValue(t, nil), nil
	}

	switch {
	case t.Is(tftypes.String):
		s, ok := v.(string)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected string, got %T", v)
		}
		return tftypes.NewValue(t, s), nil

	case t.Is(tftypes.Bool):
		b, ok := v.(bool)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected bool, got %T", v)
		}
		return tftypes.NewValue(t, b), nil

	case t.Is(tftypes.Number):
		f, ok := v.(float64)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected number, got %T", v)
		}
		return tftypes.NewValue(t, big.NewFloat(f)), nil

	case t.Is(tftypes.List{}), t.Is(tftypes.Set{}), t.Is(tftypes.Tuple{}):
		var elem tftypes.Type
		switch tt := t.(type) {
		case tftypes.List:
			elem = tt.ElementType
		case tftypes.Set:
			elem = tt.ElementType
		default:
			return tftypes.NewValue(t, nil), nil
		}
		items, ok := v.([]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected array, got %T", v)
		}
		out := make([]tftypes.Value, 0, len(items))
		for _, item := range items {
			ev, err := jsonToTf(elem, item, resource)
			if err != nil {
				return tftypes.Value{}, err
			}
			out = append(out, ev)
		}
		return tftypes.NewValue(t, out), nil

	case t.Is(tftypes.Map{}):
		elem := t.(tftypes.Map).ElementType
		entries, ok := v.(map[string]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected object, got %T", v)
		}
		out := map[string]tftypes.Value{}
		for k, item := range entries {
			ev, err := jsonToTf(elem, item, resource)
			if err != nil {
				return tftypes.Value{}, err
			}
			out[k] = ev
		}
		return tftypes.NewValue(t, out), nil

	case t.Is(tftypes.Object{}):
		attrTypes := t.(tftypes.Object).AttributeTypes
		entries, ok := v.(map[string]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected object, got %T", v)
		}
		out := make(map[string]tftypes.Value, len(attrTypes))
		for name, at := range attrTypes {
			// Absent in the response => null. Driving off the schema is the
			// whole point: it is why adding an attribute cannot silently leave
			// it unknown.
			raw := entries[apiFieldName(name)]
			if u, isUnion := unionAttrs[resource][name]; isUnion && raw != nil {
				raw = expandUnion(u, raw)
			}
			if _, isJSON := jsonEncodedAttrs[resource][name]; isJSON && raw != nil {
				// Free-form object on the wire, JSON string in Terraform.
				encoded, err := json.Marshal(raw)
				if err != nil {
					return tftypes.Value{}, fmt.Errorf("%s: %w", name, err)
				}
				raw = string(encoded)
			}
			av, err := jsonToTf(at, raw, resource)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("%s: %w", name, err)
			}
			out[name] = av
		}
		return tftypes.NewValue(t, out), nil
	}

	return tftypes.NewValue(t, nil), nil
}

// tfToJSON is the reverse, for building request bodies. Nulls and unknowns are
// dropped rather than sent, so "not configured" never becomes an explicit null.
func tfToJSON(v tftypes.Value) (any, bool) {
	if !v.IsKnown() || v.IsNull() {
		return nil, false
	}
	t := v.Type()
	switch {
	case t.Is(tftypes.String):
		var s string
		if err := v.As(&s); err != nil {
			return nil, false
		}
		return s, true
	case t.Is(tftypes.Bool):
		var b bool
		if err := v.As(&b); err != nil {
			return nil, false
		}
		return b, true
	case t.Is(tftypes.Number):
		n := new(big.Float)
		if err := v.As(&n); err != nil {
			return nil, false
		}
		f, _ := n.Float64()
		return f, true
	case t.Is(tftypes.List{}), t.Is(tftypes.Set{}), t.Is(tftypes.Tuple{}):
		var items []tftypes.Value
		if err := v.As(&items); err != nil {
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			if jv, ok := tfToJSON(item); ok {
				out = append(out, jv)
			}
		}
		return out, true
	case t.Is(tftypes.Map{}), t.Is(tftypes.Object{}):
		var entries map[string]tftypes.Value
		if err := v.As(&entries); err != nil {
			return nil, false
		}
		out := map[string]any{}
		for k, item := range entries {
			if jv, ok := tfToJSON(item); ok {
				out[k] = jv
			}
		}
		return out, true
	}
	return nil, false
}

// expandUnion turns a flat wire object into the per-variant shape the schema
// declares: {"mode":"hashed","transform_preset":X} becomes
// {"hashed":{"transform_preset":X}}. The tag is dropped — which variant key is
// populated already carries it.
func expandUnion(u unionAttr, raw any) any {
	obj, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	tag, ok := obj[u.discriminator].(string)
	if !ok {
		return raw
	}
	inner := make(map[string]any, len(obj))
	for k, v := range obj {
		if k != u.discriminator {
			inner[k] = v
		}
	}
	return map[string]any{tag: inner}
}

// collapseUnion is the reverse: find the single populated variant and flatten it
// back onto the wire object, restoring the discriminator.
func collapseUnion(u unionAttr, value any) (any, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	var chosen string
	var inner map[string]any
	for _, variant := range u.variants {
		v, present := obj[variant]
		if !present || v == nil {
			continue
		}
		vm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if chosen != "" {
			return nil, fmt.Errorf(
				"set exactly one of %v; both %q and %q are set", u.variants, chosen, variant)
		}
		chosen, inner = variant, vm
	}
	if chosen == "" {
		return nil, nil
	}
	out := make(map[string]any, len(inner)+1)
	for k, v := range inner {
		out[k] = v
	}
	out[u.discriminator] = chosen
	return out, nil
}

// secretPlaceholder is the literal the API returns in place of a credential,
// on reads and on the responses to create and update.
const secretPlaceholder = "<secret>"

// reconcileSecrets replaces concealed values in an API response with the real
// ones the caller already holds.
//
// Every read, and the response to every create and update, returns "<secret>"
// rather than the credential. Storing that verbatim breaks two ways: Terraform
// rejects an apply whose result differs from the configuration ("provider
// produced inconsistent result after apply"), and any plan afterwards compares
// the placeholder against the real configured value and proposes an update that
// can never converge.
//
// `prior` is whatever source has the true value: the request body just sent for
// a create or update, or prior state for a read. Where neither has it -- an
// import, where the resource was never configured here -- the key is dropped so
// it reads as absent. That is the honest answer: an imported credential is not
// knowable, and it is better to hold nothing than to hold a sentinel that will
// later be mistaken for a value.
//
// This walks the whole response rather than consulting a list of secret
// attributes, because the credentials sit inside `options` variants, at paths
// that differ per variant.
func reconcileSecrets(res, prior any) any {
	switch v := res.(type) {
	case string:
		if v != secretPlaceholder {
			return res
		}
		if s, ok := prior.(string); ok && s != secretPlaceholder {
			return prior
		}
		return nil
	case map[string]any:
		priorMap, _ := prior.(map[string]any)
		out := make(map[string]any, len(v))
		for key, value := range v {
			reconciled := reconcileSecrets(value, priorMap[key])
			// A concealed value with no known replacement is omitted, not
			// stored as null: for a JSON-encoded attribute the difference is
			// between a key that is absent and one explicitly set to null,
			// and the API treats those differently.
			if reconciled == nil && value != nil {
				continue
			}
			out[key] = reconciled
		}
		return out
	case []any:
		priorList, _ := prior.([]any)
		out := make([]any, len(v))
		for i, item := range v {
			var p any
			if i < len(priorList) {
				p = priorList[i]
			}
			out[i] = reconcileSecrets(item, p)
		}
		return out
	default:
		return res
	}
}

// apiToRaw converts an API response into Terraform state.
//
// `prior` carries real values for anything the response conceals; pass the
// request body after a create or update, prior state after a read, and nil when
// there is no such source.
func apiToRaw(
	ctx context.Context,
	resource string,
	s schema.Schema,
	res map[string]any,
	prior map[string]any,
) (tftypes.Value, diag.Diagnostics) {
	var diags diag.Diagnostics
	reconciled, _ := reconcileSecrets(res, prior).(map[string]any)
	raw, err := jsonToTf(s.Type().TerraformType(ctx), reconciled, resource)
	if err != nil {
		diags.AddError("decoding API response", err.Error())
		return tftypes.Value{}, diags
	}
	return raw, diags
}

// writeMode selects which set of attributes a request body may carry.
type writeMode int

const (
	forCreate writeMode = iota
	forUpdate
)

// writableJSON extracts the request body from a plan: every attribute the
// schema marks Required or Optional, skipping nulls and unknowns.
//
// Computed-only attributes are never sent. That falls out of the generated
// schema, which got Computed vs Optional from api.interfaces.yml's
// initializable/mutable flags — so the provider cannot accidentally try to
// write a server-owned field.
//
// `mode` narrows it further, and has to. Terraform has one Optional flag, but
// the API distinguishes "settable at create" from "changeable afterwards" and
// rejects a request that confuses them: Dataset.output_all_columns_as_traits
// and Target.payload_map are mutable but not initializable, so sending them on
// POST fails. resourcePerms (generated) carries both sets per resource.
func writableJSON(
	ctx context.Context,
	resource string,
	mode writeMode,
	s schema.Schema,
	raw tftypes.Value,
) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	var entries map[string]tftypes.Value
	if err := raw.As(&entries); err != nil {
		diags.AddError("reading plan", err.Error())
		return nil, diags
	}
	perms, havePerms := resourcePerms[resource]
	allowed := perms.create
	if mode == forUpdate {
		allowed = perms.update
	}
	body := map[string]any{}
	for name, a := range s.Attributes {
		if !a.IsRequired() && !a.IsOptional() {
			continue
		}
		// A resource with no generated permissions would silently send
		// everything, which is the bug this guards against; fail instead.
		if !havePerms {
			diags.AddError(
				"building request",
				"no create/update permissions generated for resource "+resource,
			)
			return nil, diags
		}
		if _, ok := allowed[name]; !ok {
			continue
		}
		v, ok := entries[name]
		if !ok {
			continue
		}
		jv, ok := tfToJSON(v)
		if !ok {
			continue
		}
		if u, isUnion := unionAttrs[resource][name]; isUnion {
			collapsed, err := collapseUnion(u, jv)
			if err != nil {
				diags.AddError("encoding "+name, err.Error())
				return nil, diags
			}
			if collapsed == nil {
				continue
			}
			jv = collapsed
		}
		if _, isJSON := jsonEncodedAttrs[resource][name]; isJSON {
			s, isStr := jv.(string)
			if !isStr {
				diags.AddError("encoding "+name, "expected a JSON string")
				return nil, diags
			}
			var decoded any
			if err := json.Unmarshal([]byte(s), &decoded); err != nil {
				diags.AddError("encoding "+name, "not valid JSON: "+err.Error())
				return nil, diags
			}
			jv = decoded
		}
		body[apiFieldName(name)] = jv
	}
	_ = ctx
	return body, diags
}

// priorJSON renders existing state in the same shape as an API response, so it
// can supply real values for anything a read conceals.
//
// Deliberately unfiltered, unlike writableJSON: a credential is often not
// writable on the operation at hand, and it is still the value we need back.
// Errors are swallowed rather than surfaced — this is a best-effort source of
// replacements, and a resource with no usable prior state should read as having
// no secret rather than fail.
func priorJSON(ctx context.Context, resource string, s schema.Schema, raw tftypes.Value) map[string]any {
	var entries map[string]tftypes.Value
	if raw.IsNull() || !raw.IsKnown() {
		return nil
	}
	if err := raw.As(&entries); err != nil {
		return nil
	}
	out := map[string]any{}
	for name := range s.Attributes {
		v, ok := entries[name]
		if !ok {
			continue
		}
		jv, ok := tfToJSON(v)
		if !ok {
			continue
		}
		if u, isUnion := unionAttrs[resource][name]; isUnion {
			collapsed, err := collapseUnion(u, jv)
			if err != nil || collapsed == nil {
				continue
			}
			jv = collapsed
		}
		if _, isJSON := jsonEncodedAttrs[resource][name]; isJSON {
			str, isStr := jv.(string)
			if !isStr {
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(str), &decoded); err != nil {
				continue
			}
			jv = decoded
		}
		out[apiFieldName(name)] = jv
	}
	_ = ctx
	return out
}
