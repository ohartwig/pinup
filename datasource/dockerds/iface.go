// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package dockerds

import "git.ole-hartwig.eu/pinup/pinup/lookup"

var (
	_ lookup.Datasource   = (*Datasource)(nil)
	_ lookup.DigestSource = (*Datasource)(nil)
)
