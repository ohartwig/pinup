// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ohartwig/pinup/model"
)

// Markdown renders a plan for a person: what will be written, what is held
// and why - with the rule that held it and when it thaws - what was skipped,
// and what went wrong. It is the job summary, the dashboard entry and the
// dry-run's answer; a plan explains why nothing happens, and this is where
// that explanation is read. Deterministic for a given plan: no clock.
func Markdown(p *model.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# pinup plan for %s\n\n", p.Repo.Path)
	fmt.Fprintf(&b, "%d dependencies in %d files, %d lookups (%d from cache), %d updates (%d held), %d branches to write.\n\n",
		p.Stats.DepsExtracted, p.Stats.FilesDiscovered, p.Stats.LookupsIssued, p.Stats.LookupsFromCache,
		p.Stats.UpdatesFound, p.Stats.UpdatesBlocked, p.Stats.BranchesPlanned)

	updates := map[string][]model.Update{}
	for _, u := range p.Updates {
		updates[u.Key()] = append(updates[u.Key()], u)
	}

	var active, held []model.Branch
	for _, br := range p.Branches {
		if br.SuppressedBy == "" {
			active = append(active, br)
		} else {
			held = append(held, br)
		}
	}
	if len(active) > 0 {
		b.WriteString("## Branches\n\n| Branch | Title | Changes |\n|---|---|---|\n")
		for _, br := range active {
			var changes []string
			for _, e := range br.Edits {
				changes = append(changes, fmt.Sprintf("`%s` %s → %s", e.File, e.Old, e.New))
			}
			for _, t := range br.Tasks {
				changes = append(changes, "`"+strings.Join(t.Command, " ")+"`")
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", br.Name, cell(br.Title), strings.Join(changes, "; "))
		}
		b.WriteString("\n")
	}
	if len(held) > 0 {
		b.WriteString("## Held\n\n| Branch | Update | Reason | Held by | Thaws |\n|---|---|---|---|---|\n")
		for _, br := range held {
			for _, k := range br.UpdateKeys {
				for _, u := range updates[k] {
					for _, blk := range u.Blocks {
						fmt.Fprintf(&b, "| `%s` | %s %s → %s | %s%s | %s | %s |\n",
							br.Name, cell(u.Dep.DepName), cell(u.Dep.CurrentValue), cell(u.NewValue), blk.Reason, note(blk), origin(blk.Org), thaw(blk.Until))
					}
				}
			}
		}
		b.WriteString("\n")
	}

	var skipped []model.Dependency
	for _, d := range p.Deps {
		if d.SkipReason != "" && !strings.HasPrefix(d.SkipReason, "up to date") {
			skipped = append(skipped, d)
		}
	}
	if len(skipped) > 0 {
		b.WriteString("## Not planned\n\n| Dependency | File | Reason |\n|---|---|---|\n")
		for _, d := range skipped {
			fmt.Fprintf(&b, "| %s %s | `%s` | %s |\n", cell(d.DepName), cell(d.CurrentValue), d.File, cell(d.SkipReason))
		}
		b.WriteString("\n")
	}
	var advisories []model.Dependency
	for _, d := range p.Deps {
		if len(d.Advisories) > 0 {
			advisories = append(advisories, d)
		}
	}
	if len(advisories) > 0 {
		b.WriteString("## Advisories\n\n| Dependency | Advisories | Fix at or above |\n|---|---|---|\n")
		for _, d := range advisories {
			ids := make([]string, 0, len(d.Advisories))
			for _, a := range d.Advisories {
				ids = append(ids, a.ID)
			}
			fmt.Fprintf(&b, "| %s %s | %s | %s |\n", cell(d.DepName), cell(d.CurrentValue), strings.Join(ids, ", "), d.VulnerabilityBound)
		}
		b.WriteString("\n")
	}
	if len(p.Warnings) > 0 {
		b.WriteString("## Warnings\n\n")
		ws := append([]model.Warning(nil), p.Warnings...)
		sort.SliceStable(ws, func(i, j int) bool { return ws[i].Stage < ws[j].Stage })
		for _, w := range ws {
			loc := ""
			if w.File != "" {
				loc = " `" + w.File + "`"
			}
			fmt.Fprintf(&b, "- **%s**%s: %s\n", w.Stage, loc, cell(w.Msg))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// cell makes a value safe inside a table row: a newline would end the row
// and a pipe would start a column.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

// note renders a block's note as code: a cron expression's asterisks are
// emphasis markers to Markdown otherwise.
func note(blk model.Block) string {
	if blk.Note == "" {
		return ""
	}
	return " `" + cell(strings.ReplaceAll(blk.Note, "`", "'")) + "`"
}

func origin(o model.Origin) string {
	if o.Rule != model.NoRule {
		return fmt.Sprintf("packageRules[%d]", o.Rule)
	}
	if o.Source != "" {
		return o.Source
	}
	return "—"
}

func thaw(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}
