// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Command pinup keeps dependency references current across a fleet of
// repositories. It replaces the Renovate runner; see docs/tech-spec.md.
package main

import (
	"fmt"
	"os"

	// The binary carries the zone database: every schedule in the estate
	// is written in Europe/Berlin, and a job image without tzdata turned
	// "after 1am and before 6am" into an error on the runner's first
	// scheduled run. Half a megabyte, and no image can take it away.
	_ "time/tzdata"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pinup:", err)
		os.Exit(1)
	}
}
