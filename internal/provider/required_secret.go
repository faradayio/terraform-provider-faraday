package provider

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// requiredSecret is a credential the API requires when a resource is created,
// named by its attribute path from the resource root, such as
// {"options", "snowflake", "password"}.
//
// The schema cannot mark it Required: the API never returns a secret, so an
// imported resource has no value for it and every import would fail to plan.
// So it is optional in the schema and checked when a create is planned.
type requiredSecret []string

func (s requiredSecret) attributePath() path.Path {
	p := path.Root(s[0])
	for _, name := range s[1:] {
		p = p.AtName(name)
	}
	return p
}

// isMissingFrom reports whether config leaves the secret out while setting
// every object that contains it. An option variant nobody chose is null, and
// its secrets are not required; neither is a value not yet known at plan time.
func (s requiredSecret) isMissingFrom(config tftypes.Value) bool {
	current := config
	for _, name := range s {
		if current.IsNull() || !current.IsKnown() {
			return false
		}
		var entries map[string]tftypes.Value
		if err := current.As(&entries); err != nil {
			panic(fmt.Sprintf("required secret %v: %s is not an object: %v", []string(s), name, err))
		}
		next, ok := entries[name]
		if !ok {
			panic(fmt.Sprintf("required secret %v: no attribute %q in the schema", []string(s), name))
		}
		current = next
	}
	return current.IsNull()
}
