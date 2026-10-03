// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"testing"

	"github.com/ohartwig/pinup/model"
)

// A security lock refresh names the packages it is for, each advisory with
// its own fix: "security fix" alone says nothing about a lock of 260.
func TestASecurityLockRefreshNamesItsPackages(t *testing.T) {
	u := model.Update{Type: model.UpdateLockFileMaintenance, SecurityFix: true, Dep: model.Dependency{Advisories: []model.Advisory{
		{ID: "GHSA-a", Package: "guzzlehttp/psr7", Installed: "2.6.0", Fixed: "2.6.3"},
		{ID: "GHSA-b", Package: "guzzlehttp/psr7", Installed: "2.6.0"},
		{ID: "CVE-2026-1", Package: "symfony/http-kernel", Installed: "7.3.1", Fixed: "7.3.2"},
	}}}
	want := "security fix: `guzzlehttp/psr7` 2.6.0 (GHSA-a fixed in 2.6.3, GHSA-b); `symfony/http-kernel` 7.3.1 (CVE-2026-1 fixed in 7.3.2)"
	if got := noteLinks(u); got != want {
		t.Errorf("noteLinks =\n%q\nwant\n%q", got, want)
	}
}
