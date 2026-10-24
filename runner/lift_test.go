// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/fake/platformfake"
	"github.com/ohartwig/pinup/model"
)

// flatLock reads the test's lock shape: a JSON object of name -> version.
func flatLock(raw []byte) (map[string]string, error) {
	var out map[string]string
	err := json.Unmarshal(raw, &out)
	return out, err
}

// A security lock refresh is read back after it ran. The case it exists
// for is development/moselwal/sitepackage-base, 2026-10-20..23: five
// refreshes titled [security] for fast-uri 3.1.8 merged and every one left
// 3.1.8 in the lock. A lift that did not move is named in the request and
// on the dashboard, and a request whose only security content stayed put
// is not titled [security].
func TestASecurityRefreshSaysWhetherItLiftedTheAdvisory(t *testing.T) {
	refresh := model.Task{Kind: model.TaskLockRefresh, Manager: "npm", Command: []string{"npm", "update", "--package-lock-only"},
		ExecutionMode: model.ExecBranch, FileFilters: []string{"package-lock.json", "npm-shrinkwrap.json"}, AllowedBy: -1}
	lift := refresh
	lift.Command = []string{"npm", "update", "fast-uri", "--package-lock-only", "--min-release-age=0"}
	lift.Lifts = []model.Lift{{Package: "fast-uri", Installed: "3.1.8", Fixed: "3.1.9"}}
	const title = "chore(deps): refresh lock file [security]"

	for _, c := range []struct {
		name      string
		lock      string
		edits     bool
		noReader  bool
		wantTitle string
		unlifted  int
		warning   string
	}{
		{name: "lifted", lock: `{"fast-uri": "3.1.9", "ajv": "8.17.1"}`, wantTitle: title},
		{name: "left in place", lock: `{"fast-uri": "3.1.8", "ajv": "8.18.0"}`, wantTitle: "chore(deps): refresh lock file", unlifted: 1,
			warning: "transitive fast-uri 3.1.8 not lifted by the security lock refresh (fixed in 3.1.9)"},
		// A branch with edits carries a direct bump that its own install
		// checks; its title stays, the note and the warning still come.
		{name: "left in place beside an edit", lock: `{"fast-uri": "3.1.8"}`, edits: true, wantTitle: title, unlifted: 1,
			warning: "not lifted by the security lock refresh"},
		// No reader, no claim: the title stays and the plan says the
		// check did not happen rather than that it passed.
		{name: "unchecked", lock: `{"fast-uri": "3.1.8"}`, noReader: true, wantTitle: title, warning: "lifts not checked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, repo := fixture(t)
			pf := &platformfake.Platform{}
			b := model.Branch{Name: "renovate/lock-file-maintenance", Title: title, Tasks: []model.Task{refresh, lift}}
			if c.edits {
				b.Name, b.UpdateKeys, b.Edits = "renovate/alpine-3.x", []string{"Containerfile|alpine|3.20"}, []model.Edit{edit("3.20", "3.21")}
			}
			o := options(repo, pf)
			o.Tasks = &fakeTasks{writes: map[string]string{"package-lock.json": c.lock + "\n"}}
			if !c.noReader {
				o.LockedVersions = flatLock
			}
			p := plan(b)
			outs, err := Execute(context.Background(), p, o)
			if err != nil {
				t.Fatal(err)
			}
			if len(outs) != 1 || outs[0].Action != "created" || len(pf.MRs) != 1 {
				t.Fatalf("outcomes %+v, %d requests", outs, len(pf.MRs))
			}
			mr := pf.MRs[0]
			if mr.Title != c.wantTitle {
				t.Errorf("title %q, want %q", mr.Title, c.wantTitle)
			}
			if got := len(p.Branches[0].Unlifted); got != c.unlifted {
				t.Errorf("%d unlifted, want %d", got, c.unlifted)
			}
			if noted := strings.Contains(mr.Description, "**Not lifted:** `fast-uri` 3.1.8 (fixed in 3.1.9)"); noted != (c.unlifted > 0) {
				t.Errorf("description notes the lift: %v, want %v\n%s", noted, c.unlifted > 0, mr.Description)
			}
			var warned bool
			for _, w := range p.Warnings {
				if c.warning != "" && strings.Contains(w.Msg, c.warning) {
					warned = true
					if c.unlifted > 0 && w.File != "package-lock.json" {
						t.Errorf("warning on %q, want package-lock.json", w.File)
					}
				}
			}
			if warned != (c.warning != "") {
				t.Errorf("warnings %+v, want one containing %q", p.Warnings, c.warning)
			}
		})
	}
}
