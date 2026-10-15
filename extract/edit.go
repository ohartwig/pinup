// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"context"
	"fmt"

	"github.com/ohartwig/pinup/model"
)

// MultiEditor is a Manager whose one update may have to move two separate
// spans. A regex match can bind the value and the digest of one reference
// on two lines - a koh/gitlab-checkout step in a melange recipe writes
// `tag:` and `expected-commit:` one under the other - and Edit, which
// hands back one byte range, cannot carry that. Edits hands back every
// range the update touches; EditsOf prefers it where it exists.
type MultiEditor interface {
	Edits(ctx context.Context, f File, up model.Update) ([]model.Edit, error)
}

// EditsOf asks a manager for every edit one update needs: Edits where the
// manager offers it, Edit otherwise.
func EditsOf(ctx context.Context, m Manager, f File, up model.Update) ([]model.Edit, error) {
	if me, ok := m.(MultiEditor); ok {
		return me.Edits(ctx, f, up)
	}
	e, err := m.Edit(ctx, f, up)
	if err != nil {
		return nil, err
	}
	return []model.Edit{e}, nil
}

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
//     contiguous with a single `@` between; a pinDigest update appends a
//     digest to a reference that had none - and only that update type does,
//     since any release may carry a digest (a tag's commit id, say).
//
// Value and digest written apart - two lines, two spans - need two edits,
// which only EditRefs can hand back. EditRef refuses that case: a manager
// that returns one edit must not move the tag and leave the commit, which
// is exactly the half-update the second rule forbids. Measured 2026-10-14/15
// in devops/wolfi-packages: seven merge requests "update caddy-fs-s3cache
// to v1.0.2" whose diff was the epoch alone, because the tag edit was
// refused here and the branch went out with its post-upgrade task.
func EditRef(manager string, f File, up model.Update) (model.Edit, error) {
	edits, err := EditRefs(manager, f, up)
	if err != nil {
		return model.Edit{}, err
	}
	if len(edits) != 1 {
		return model.Edit{}, fmt.Errorf("%s: %s: value and digest of %s are not written as value@digest; cannot replace both in one edit (the manager has to hand back two, see MultiEditor)",
			manager, f.Path, up.Dep.DepName)
	}
	return edits[0], nil
}

// EditRefs is EditRef for a reference whose value and digest may sit apart:
// it hands back one edit for the common shapes and two - value span, then
// digest span - when both move and are not written as value@digest.
func EditRefs(manager string, f File, up model.Update) ([]model.Edit, error) {
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
	digestKnown := up.NewDigest != ""
	digestChanges := digestKnown && up.NewDigest != dep.CurrentDigest

	switch {
	case hasDigest && valueChanges && !digestKnown:
		return nil, fmt.Errorf("%s: %s: %s to %s carries no digest for the new value; a reference pinned by digest is not moved by tag alone",
			manager, f.Path, dep.DepName, up.NewValue)

	case hasDigest && valueChanges && !digestChanges:
		// The new tag resolves to the very digest already pinned - a
		// release that changed nothing in the image (measured:
		// devops/images/c2patool 2.1.10 and 2.1.11 share sha256:79fec605,
		// 2026-09-14). The tag moves, the digest stays, as Renovate writes
		// it; the planner looked the digest up, so this is not the case
		// above.
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return nil, err
		}
		return []model.Edit{{
			File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
			Old: dep.CurrentValue, New: up.NewValue, Manager: manager,
		}}, nil

	case hasDigest && valueChanges && digestChanges:
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return nil, err
		}
		if err := verify(l.DigestStart, l.DigestEnd, dep.CurrentDigest); err != nil {
			return nil, err
		}
		if l.DigestStart == l.ValueEnd+1 && src[l.ValueEnd] == '@' {
			return []model.Edit{{
				File: f.Path, Start: l.ValueStart, End: l.DigestEnd,
				Old: dep.CurrentValue + "@" + dep.CurrentDigest, New: up.NewValue + "@" + up.NewDigest, Manager: manager,
			}}, nil
		}
		// Written apart: two edits, in file order, neither overlapping the
		// other. The applier takes them as it takes two dependencies' edits
		// in one file.
		if l.ValueEnd > l.DigestStart && l.DigestEnd > l.ValueStart {
			return nil, fmt.Errorf("%s: %s: value and digest of %s overlap at [%d:%d] and [%d:%d]",
				manager, f.Path, dep.DepName, l.ValueStart, l.ValueEnd, l.DigestStart, l.DigestEnd)
		}
		value := model.Edit{File: f.Path, Start: l.ValueStart, End: l.ValueEnd, Old: dep.CurrentValue, New: up.NewValue, Manager: manager}
		digest := model.Edit{File: f.Path, Start: l.DigestStart, End: l.DigestEnd, Old: dep.CurrentDigest, New: up.NewDigest, Manager: manager}
		if digest.Start < value.Start {
			return []model.Edit{digest, value}, nil
		}
		return []model.Edit{value, digest}, nil

	case hasDigest && digestChanges:
		if err := verify(l.DigestStart, l.DigestEnd, dep.CurrentDigest); err != nil {
			return nil, err
		}
		return []model.Edit{{
			File: f.Path, Start: l.DigestStart, End: l.DigestEnd,
			Old: dep.CurrentDigest, New: up.NewDigest, Manager: manager,
		}}, nil

	case !hasDigest && up.Type == model.UpdatePinDigest && up.NewDigest != "":
		// Pinning a digest onto a reference that had none - only when the
		// update says so. Every release may carry a digest (a tag's commit
		// id, say) and an unpinned reference stays unpinned by default.
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return nil, err
		}
		newValue := dep.CurrentValue
		if valueChanges {
			newValue = up.NewValue
		}
		return []model.Edit{{
			File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
			Old: dep.CurrentValue, New: newValue + "@" + up.NewDigest, Manager: manager,
		}}, nil

	case valueChanges:
		if l.ValueStart >= l.ValueEnd {
			return nil, fmt.Errorf("%s: %s: no editable range recorded for %s", manager, f.Path, dep.DepName)
		}
		if err := verify(l.ValueStart, l.ValueEnd, dep.CurrentValue); err != nil {
			return nil, err
		}
		return []model.Edit{{
			File: f.Path, Start: l.ValueStart, End: l.ValueEnd,
			Old: dep.CurrentValue, New: up.NewValue, Manager: manager,
		}}, nil

	default:
		return nil, fmt.Errorf("%s: update for %s names no change to apply", manager, dep.DepName)
	}
}
