// SPDX-License-Identifier: AGPL-3.0-or-later

// Command proxmox is the OpenTofu/Terraform provider plugin entrypoint for the
// Proxmox product family — PVE, PBS, PMG, and PDM — via the shared /api2/json
// REST API. It is generic over that API surface (proxmox_object addresses any
// path; proxmox_task issues any async operation), giving 100% feature coverage
// without per-feature code.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/JamesonRGrieve/tofu-proxmox/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jamesonrgrieve/proxmox",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
