package main

import (
	"context"
	"log"

	"github.com/faradayio/terraform-provider-faraday/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is the provider version, overridden at build time with
//
//	go build -ldflags="-X main.version=$(cat VERSION)"
//
// The VERSION file is the source of truth: `audit.mts` reads it to lay out the
// filesystem mirror it installs this provider from, and a mirror is versioned
// by construction. The "dev" default is what an un-stamped local `go build`
// reports, which is honest -- that binary is not any released version.
// commit is set by GoReleaser when it builds a release. A local build leaves it empty.
var (
	version = "dev"
	commit  = ""
)

func main() {
	// GoReleaser sets commit with -X. Nothing reads it; the reference keeps it
	// from looking unused to a checker that cannot see the linker flag.
	_ = commit
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/faradayio/faraday",
	})
	if err != nil {
		log.Fatal(err)
	}
}
