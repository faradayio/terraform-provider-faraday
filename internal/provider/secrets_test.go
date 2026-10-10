package provider

// The API answers with "<secret>" in place of every credential, on reads and on
// the responses to create and update alike. Storing that verbatim fails twice
// over: Terraform rejects an apply whose result differs from the configuration,
// and every later plan compares the placeholder against the real value and
// proposes an update that cannot converge. These pin the substitution rules.

import (
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

func TestCreateAndUpdateRestoreFromTheRequest(t *testing.T) {
	// What the API returns after accepting a password, and what we sent.
	res := map[string]any{
		"id":      "abc",
		"name":    "warehouse",
		"options": map[string]any{"type": "snowflake", "user": "svc", "password": secretPlaceholder},
	}
	sent := map[string]any{
		"name":    "warehouse",
		"options": map[string]any{"type": "snowflake", "user": "svc", "password": "hunter2"},
	}

	got := reconcileSecrets(res, sent)
	opts := got.(map[string]any)["options"].(map[string]any)
	if opts["password"] != "hunter2" {
		t.Errorf("password = %v, want the value we sent", opts["password"])
	}
	if opts["user"] != "svc" {
		t.Errorf("non-secret values must pass through untouched, got %v", opts["user"])
	}
	if got.(map[string]any)["id"] != "abc" {
		t.Error("server-owned values must pass through untouched")
	}
}

func TestReadRestoresFromPriorState(t *testing.T) {
	res := map[string]any{"options": map[string]any{"password": secretPlaceholder}}
	prior := map[string]any{"options": map[string]any{"password": "hunter2"}}

	got := reconcileSecrets(res, prior).(map[string]any)
	if got["options"].(map[string]any)["password"] != "hunter2" {
		t.Error("a read must restore the credential from prior state")
	}
}

func TestImportStoresNothingRatherThanThePlaceholder(t *testing.T) {
	// An imported resource was never configured here, so neither the request
	// nor prior state has the value. The honest answer is absence: a stored
	// "<secret>" would later be mistaken for a real password and written back.
	res := map[string]any{"options": map[string]any{"type": "snowflake", "password": secretPlaceholder}}

	got := reconcileSecrets(res, nil).(map[string]any)
	opts := got["options"].(map[string]any)
	if _, present := opts["password"]; present {
		t.Errorf("password must be absent after import, got %v", opts["password"])
	}
	if opts["type"] != "snowflake" {
		t.Error("the rest of the object must survive")
	}
}

func TestAPlaceholderInPriorIsNotTreatedAsAValue(t *testing.T) {
	// Prior state can itself hold a placeholder if an earlier version of this
	// provider stored one. Copying it forward would make the bug permanent.
	res := map[string]any{"password": secretPlaceholder}
	prior := map[string]any{"password": secretPlaceholder}

	got := reconcileSecrets(res, prior).(map[string]any)
	if _, present := got["password"]; present {
		t.Errorf("a placeholder in prior state must not be carried forward, got %v", got["password"])
	}
}

func TestNestedAndListValuesAreWalked(t *testing.T) {
	res := map[string]any{
		"outer": map[string]any{
			"items": []any{
				map[string]any{"token": secretPlaceholder},
				map[string]any{"token": "plain"},
			},
		},
	}
	prior := map[string]any{
		"outer": map[string]any{
			"items": []any{
				map[string]any{"token": "real"},
				map[string]any{"token": "plain"},
			},
		},
	}

	got := reconcileSecrets(res, prior).(map[string]any)
	items := got["outer"].(map[string]any)["items"].([]any)
	if items[0].(map[string]any)["token"] != "real" {
		t.Error("a concealed value inside a list must be restored")
	}
	if items[1].(map[string]any)["token"] != "plain" {
		t.Error("untouched list entries must survive")
	}
}

func TestAResponseWithoutSecretsIsUnchanged(t *testing.T) {
	res := map[string]any{
		"name":   "x",
		"count":  float64(3),
		"nested": map[string]any{"a": true},
		"list":   []any{"one", "two"},
	}
	got := reconcileSecrets(res, nil)
	if !reflect.DeepEqual(got, res) {
		t.Errorf("reconcile altered a response with no secrets:\n got %#v\nwant %#v", got, res)
	}
}

// countSensitive walks a schema's attributes, including nested ones, and
// returns how many are marked Sensitive.
func countSensitive(attrs map[string]schema.Attribute) int {
	n := 0
	for _, attr := range attrs {
		if attr.IsSensitive() {
			n++
		}
		switch nested := attr.(type) {
		case schema.SingleNestedAttribute:
			n += countSensitive(nested.Attributes)
		case schema.ListNestedAttribute:
			n += countSensitive(nested.NestedObject.Attributes)
		case schema.SetNestedAttribute:
			n += countSensitive(nested.NestedObject.Attributes)
		case schema.MapNestedAttribute:
			n += countSensitive(nested.NestedObject.Attributes)
		}
	}
	return n
}

func resourceSchema(t *testing.T, name string) schema.Schema {
	t.Helper()
	for i := range resourceDefs {
		if resourceDefs[i].name == name {
			return resourceDefs[i].schemaFn(t.Context())
		}
	}
	t.Fatalf("no resource %q", name)
	return schema.Schema{}
}

func TestEachCredentialIsIndividuallySensitive(t *testing.T) {
	// `options` used to be an opaque JSON string, so the only way to say it
	// held a password was to mark the whole string Sensitive. Now that the
	// union is expanded, every x-secret member carries its own flag and the
	// counts have to match the spec exactly -- too few means a credential is
	// being logged in plaintext, and the blunt marking is gone as a backstop.
	for _, tc := range []struct {
		resource string
		want     int // x-secret members across all variants of `options`
	}{
		{"connection", 30},
		{"dataset", 0},
		{"target", 3},
	} {
		s := resourceSchema(t, tc.resource)
		options, ok := s.Attributes["options"]
		if !ok {
			t.Fatalf("%s has no options attribute", tc.resource)
		}
		if _, isString := options.(schema.StringAttribute); isString {
			t.Fatalf("%s.options is still a JSON string; the union did not expand", tc.resource)
		}
		if got := countSensitive(s.Attributes); got != tc.want {
			t.Errorf("%s: %d Sensitive attributes, want %d", tc.resource, got, tc.want)
		}
	}
}