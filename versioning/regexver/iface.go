// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package regexver

import "github.com/ohartwig/pinup/versioning"

var (
	_ versioning.Parameterised = (*Scheme)(nil)
	_ versioning.Versioning    = (*Scheme)(nil)
)
