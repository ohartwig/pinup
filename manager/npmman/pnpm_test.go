// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npmman

import (
	"maps"
	"strings"
	"testing"
)

// The shape of nozzleops/platform's lock: lockfileVersion 9.0, the root and
// each workspace member an importer, a version carrying the peer context it
// was resolved in, a sibling linked rather than versioned.
const pnpmWorkspaceLock = `lockfileVersion: '9.0'

settings:
  autoInstallPeers: true
  excludeLinksFromLockfile: false

importers:

  .:
    devDependencies:
      turbo:
        specifier: ^2.5.0
        version: 2.5.8

  apps/web:
    dependencies:
      '@hookform/resolvers':
        specifier: ^5.0.0
        version: 5.7.1(@standard-schema/spec@1.1.0)(react-hook-form@7.72.0(react@19.2.4))
      '@nozzleops/shared':
        specifier: workspace:*
        version: link:../../packages/shared
      clsx:
        specifier: ^2.1.0
        version: 2.1.1
    devDependencies:
      '@types/node':
        specifier: 22.20.1
        version: 22.20.1
    optionalDependencies:
      fsevents:
        specifier: ^2.3.3
        version: 2.3.3

packages:

  clsx@2.1.1:
    resolution: {integrity: sha512-x}
`

// Before lockfileVersion 6 a single project kept its maps at the top level,
// each version a plain string.
const pnpmSingleProjectLock = `lockfileVersion: 5.4

specifiers:
  lodash: ^4.17.20

dependencies:
  lodash: 4.17.21

devDependencies:
  typescript: 5.6.3_@types+node@22.20.1
`

// A pnpm-lock.yaml names each importer's resolved versions: the root's
// under ".", a member's under its path; the peer context and linked
// siblings are not versions. A member the lock does not know is an error,
// not an empty answer - that is how "nothing locked" was read before.
func TestAPnpmLockNamesEachImportersVersions(t *testing.T) {
	for _, tc := range []struct {
		name, lock, member string
		want               map[string]string
		errHas             string
	}{
		{name: "the workspace root", lock: pnpmWorkspaceLock, member: "",
			want: map[string]string{"turbo": "2.5.8"}},
		{name: "a member, peer context and link dropped", lock: pnpmWorkspaceLock, member: "apps/web",
			want: map[string]string{"@hookform/resolvers": "5.7.1", "clsx": "2.1.1", "@types/node": "22.20.1", "fsevents": "2.3.3"}},
		{name: "a member the lock does not know", lock: pnpmWorkspaceLock, member: "apps/admin",
			errHas: `no importer "apps/admin"`},
		{name: "the single-project shape of lockfileVersion 5", lock: pnpmSingleProjectLock, member: "",
			want: map[string]string{"lodash": "4.17.21", "typescript": "5.6.3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LockedVersionsFor([]byte(tc.lock), tc.member)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want %q", err, tc.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("versions = %v, want %v", got, tc.want)
			}
		})
	}
}
