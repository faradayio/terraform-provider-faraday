package provider

// The create/update split has a silent failure mode: send a field POST does not
// accept and the API rejects the whole create, but nothing in the schema or the
// plan hints at why. These pin the generated permissions to the facts in
// api.interfaces.yml that produced them.

import "testing"

func perms(t *testing.T, resource string) attrPerms {
	t.Helper()
	p, ok := resourcePerms[resource]
	if !ok {
		t.Fatalf("no generated permissions for %q", resource)
	}
	return p
}

func TestMutableButNotInitializableIsUpdateOnly(t *testing.T) {
	// The two Seamus's review named. Both are `initializable: false,
	// mutable: true`, so a create that sends them fails.
	cases := []struct{ resource, attr string }{
		{"dataset", "output_all_columns_as_traits"},
		{"target", "payload_map"},
	}
	for _, tc := range cases {
		t.Run(tc.resource+"."+tc.attr, func(t *testing.T) {
			p := perms(t, tc.resource)
			if _, ok := p.create[tc.attr]; ok {
				t.Errorf("%s is initializable:false — a create must not send it", tc.attr)
			}
			if _, ok := p.update[tc.attr]; !ok {
				t.Errorf("%s is mutable:true — an update must send it", tc.attr)
			}
		})
	}
}

func TestCreateOnlyFieldsAreNotSentOnUpdate(t *testing.T) {
	// The mirror image: initializable but not mutable. Sending one on PATCH is
	// rejected, and these are also the RequiresReplace attributes, so getting
	// this wrong turns a replace into a failed update.
	cases := []struct{ resource, attr string }{
		{"dataset", "connection_id"},
		{"persona_set", "cohort_id"},
		{"target", "scope_id"},
	}
	for _, tc := range cases {
		t.Run(tc.resource+"."+tc.attr, func(t *testing.T) {
			p := perms(t, tc.resource)
			if _, ok := p.create[tc.attr]; !ok {
				t.Errorf("%s is initializable — a create must send it", tc.attr)
			}
			if _, ok := p.update[tc.attr]; ok {
				t.Errorf("%s is mutable:false — an update must not send it", tc.attr)
			}
		})
	}
}

func TestInitializableDefaultsToMutable(t *testing.T) {
	// `initializable` omitted means "same as mutable", not false. Reading it as
	// false made these generate as Optional+Computed and get dropped from the
	// create body, so the API rejected every create with a missing required
	// field. Both are `required: true, mutable: true` with no `initializable`.
	cases := []struct{ resource, attr string }{
		{"dataset", "identity_sets"},
		{"scope", "preview"},
	}
	for _, tc := range cases {
		t.Run(tc.resource+"."+tc.attr, func(t *testing.T) {
			p := perms(t, tc.resource)
			if _, ok := p.create[tc.attr]; !ok {
				t.Errorf("%s is required in POST — a create must send it", tc.attr)
			}
		})
	}
}

func TestServerOwnedFieldsAreNeverSent(t *testing.T) {
	// Neither initializable nor mutable. These are the ones an audit must never
	// try to write back after importing them.
	for _, attr := range []string{"id", "created_at", "updated_at", "status", "archived_at"} {
		for resource, p := range resourcePerms {
			if _, ok := p.create[attr]; ok {
				t.Errorf("%s.%s is server-owned but appears in create", resource, attr)
			}
			if _, ok := p.update[attr]; ok {
				t.Errorf("%s.%s is server-owned but appears in update", resource, attr)
			}
		}
	}
}

func TestEveryResourceHasGeneratedPerms(t *testing.T) {
	// writableJSON refuses to build a body for a resource with no generated
	// permissions rather than silently sending everything, so a missing entry
	// breaks that resource entirely.
	for _, def := range resourceDefs {
		if _, ok := resourcePerms[def.name]; !ok {
			t.Errorf("resource %q has no generated permissions", def.name)
		}
	}
}
