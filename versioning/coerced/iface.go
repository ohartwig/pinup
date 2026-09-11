// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package coerced

import "git.ole-hartwig.eu/pinup/pinup/versioning"

var _ versioning.Versioning = (*Scheme)(nil)
