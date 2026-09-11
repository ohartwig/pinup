// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package extract

import (
	"fmt"

	"git.ole-hartwig.eu/pinup/pinup/model"
)

// EditRef turns an update into the byte-range edit of a versioned reference
// - `value`, `value@digest` or `@digest` - the way every text manager that
// records a Locus needs it. The rules, in order:
//
//   - the bytes at every locus the edit touches must still be what was
//     extracted; a file that changed underneath the run is refused, not
//     patched.
//   - a reference that carries a digest is never given a new value without
//     a new digest. `newtag@olddigest` is not a partial update: a runtime
//     pulls by digest and ignores the tag, so the file would claim a
//     version it does not run. The planner is responsible for supplying
//     the digest; this is the last line of defence.
//   - a digest alone moves the digest span; a value alone moves the value
//     span; both move the joined `value@digest` span, which must be
//     contiguous with a single `@` between; a digest onto a reference that
//     had none is appended to the value.
func EditRef(manager string, f File, up model.Update) (model.Edit, error) {
	dep := up.Dep
	src := f.Content
	l := dep.Locus

	verify := func(start, end int, want string) error {
		if start < 0 || end < 0 || start > end || end > len(src) {
			return fmt.Errorf("%s: %s: locus [%d:%d] is out of range (file has %d bytes)", manager, f.Path, start, end, len(src))
		}
		if got := string(src[start:end]); got != want {
			return fmt.Errorf("%s: %s changed since extraction: expected %q at [%d:%d], found %q", manager, f.Path, want, start, end, got)
		}
		return nil
	}

	hasDigest := l.DigestStart != model.NoDigest && l.DigestEnd != model.NoDigest && dep.CurrentDigest != ""
	valueChanges := up.NewValue != "" && up.NewValue != dep.CurrentValue
	digestChanges := up.NewDigest != "" && up.NewDigest != dep.CurrentDigest

	switch {
	case hasDigest && valueChanges && !digestChanges:
		return model.Edit{}, fmt.Errorf("%s: %s: %s to %s carries no digest for the new value; a reference pinned by digest is not moved by tag alone",
			manager, f.Path, dep.DepName, up.NewValue)

	case hasDigest && valueChanges && digestChanges:
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return model.Edit{}, err
		}
		if err := verify(l.DigestStart, l.DigestEnd, dep.CurrentDigest); err != nil {
			return model.Edit{}, err
		}
		if l.DigestStart != l.ValueEnd+1 || src[l.ValueEnd] != '@' {
			return model.Edit{}, fmt.Errorf("%s: %s: value and digest of %s are not written as value@digest; cannot replace both in one edit",
				manager, f.Path, dep.DepName)
		}
		return model.Edit{
			File: f.Path, Start: l.ValueStart, End: l.DigestEnd,
			Old: dep.CurrentValue + "@" + dep.CurrentDigest, New: up.NewValue + "@" + up.NewDigest, Manager: manager,
		}, nil

	case hasDigest && digestChanges:
		if err := verify(l.DigestStart, l.DigestEnd, dep.CurrentDigest); err != nil {
			return model.Edit{}, err
		}
		return model.Edit{
			File: f.Path, Start: l.DigestStart, End: l.DigestEnd,
			Old: dep.CurrentDigest, New: up.NewDigest, Manager: manager,
		}, nil

	case !hasDigest && up.NewDigest != "":
		// Pinning a digest onto a reference that had none: appended to the
		// value, since there is no digest span to target yet.
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return model.Edit{}, err
		}
		newValue := dep.CurrentValue
		if valueChanges {
			newValue = up.NewValue
		}
		return model.Edit{
			File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
			Old: dep.CurrentValue, New: newValue + "@" + up.NewDigest, Manager: manager,
		}, nil

	case valueChanges:
		if l.ValueStart >= l.ValueEnd {
			return model.Edit{}, fmt.Errorf("%s: %s: no editable range recorded for %s", manager, f.Path, dep.DepName)
		}
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return model.Edit{}, err
		}
		return model.Edit{
			File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
			Old: dep.CurrentValue, New: up.NewValue, Manager: manager,
		}, nil

	default:
		return model.Edit{}, fmt.Errorf("%s: update for %s names no change to apply", manager, dep.DepName)
	}
}
