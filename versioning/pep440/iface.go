// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package pep440

import "github.com/ohartwig/pinup/versioning"

var _ versioning.Versioning = (*Scheme)(nil)
