// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// securitySuffix is what a vulnerabilityAlerts title ends in.
const securitySuffix = " [security]"

// unlifted reads back the lock a security lock refresh ran on and returns
// the lifts it left in place: each package the lock still pins at the
// vulnerable version. The planner announced the fix; only the lock after
// the tool ran can say whether it happened. Between 2026-10-20 and
// 2026-10-23 development/moselwal/sitepackage-base merged five lock
// refreshes titled [security] for fast-uri 3.1.8 and every one of them
// left 3.1.8 where it was.
//
// The lock is the first of the task's file filters present in its
// directory. An error means the lock could not be read back: nothing is
// claimed either way.
func (o Options) unlifted(t model.Task) ([]model.Lift, error) {
	if len(t.Lifts) == 0 {
		return nil, nil
	}
	if o.LockedVersions == nil {
		return nil, errors.New("no lock reader to check the lifts against")
	}
	var raw []byte
	var name string
	for _, f := range t.FileFilters {
		b, err := os.ReadFile(filepath.Join(o.Repo.Dir, t.Dir, f))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		raw, name = b, f
		break
	}
	if name == "" {
		return nil, fmt.Errorf("none of %s in %q after the refresh", strings.Join(t.FileFilters, ", "), t.Dir)
	}
	versions, err := o.LockedVersions(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var out []model.Lift
	for _, l := range t.Lifts {
		if versions[l.Package] == l.Installed {
			out = append(out, l)
		}
	}
	return out, nil
}

// unliftedWarning is the dashboard's line for a lift the refresh left in
// place. The release age is set aside on the command (plugin.SecurityLift),
// so what is left is a constraint: something that requires the package
// does not admit the fixed version. gitlab-housekeeping reads "not lifted"
// to tell this from an orphan nobody is working on.
func unliftedWarning(t model.Task, l model.Lift) model.Warning {
	fix := "no fixed version known"
	if l.Fixed != "" {
		fix = "fixed in " + l.Fixed
	}
	return model.Warning{Stage: "refresh", File: filepath.ToSlash(filepath.Join(t.Dir, t.FileFilters[0])), Msg: fmt.Sprintf(
		"transitive %s %s not lifted by the security lock refresh (%s): still locked after `%s` - whatever requires it does not admit the fix",
		l.Package, l.Installed, fix, strings.Join(t.Command, " "))}
}

// dropSecurityTitle takes the [security] claim off a branch whose only
// security content was lifts and none of them moved: what is left is a
// routine refresh and is titled as one. A branch with edits keeps its
// title - a direct security bump is checked by its own install.
func dropSecurityTitle(b *model.Branch, lifts, unlifted int) {
	if lifts == 0 || unlifted < lifts || len(b.Edits) > 0 {
		return
	}
	if title, ok := strings.CutSuffix(b.Title, securitySuffix); ok {
		b.Title = title
	}
}

// unliftedNote opens the description of a branch that left lifts in place.
func unliftedNote(b *model.Branch) string {
	if len(b.Unlifted) == 0 {
		return ""
	}
	var parts []string
	for _, l := range b.Unlifted {
		p := fmt.Sprintf("`%s` %s", l.Package, l.Installed)
		if l.Fixed != "" {
			p += " (fixed in " + l.Fixed + ")"
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("> **Not lifted:** %s - still locked after the security refresh. Whatever requires it does not admit the fix; this request does not clear that advisory.\n\n",
		strings.Join(parts, "; "))
}
