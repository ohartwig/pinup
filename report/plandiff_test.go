// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// The k3s group of koh-gitops!2923 as two plans: before, the version and
// the image were two branches and the image was not tracked; after, one
// group branch carries both. PlanDiff has to say that in the words a
// reviewer reads, section by section.
func TestPlanDiffSaysWhatAConfigurationChangeDoes(t *testing.T) {
	upd := func(dep, from, to string, blocks ...model.Block) model.Update {
		return model.Update{DepKey: dep + "|" + from, NewValue: to, Dep: model.Dependency{DepName: dep, CurrentValue: from}, Blocks: blocks}
	}
	k3s := upd("k3s-io/k3s", "v1.36.4+k3s1", "v1.37.0+k3s1")
	image := upd("k3s-upgrade", "v1.36.4-k3s1", "v1.37.0-k3s1")
	grafana := upd("monitoring-grafana", "2.3.19", "2.3.21",
		model.Block{Reason: model.BlockConcurrentLimit, Note: "prConcurrentLimit 10: 10 merge requests already open", Org: model.Origin{Source: "config", Rule: model.NoRule}})

	base := &model.Plan{
		Branches: []model.Branch{
			{Name: "pinup/k3s-io-k3s-1.x", Title: "update k3s-io/k3s to v1.37.0+k3s1", UpdateKeys: []string{k3s.Key()}, Labels: []string{"needs-runbook"}},
			{Name: "pinup/grafana", Title: "update grafana", UpdateKeys: []string{grafana.Key()}, SuppressedBy: model.BlockConcurrentLimit},
			{Name: "pinup/curl", Title: "update curl", Automerge: true},
		},
		Updates: []model.Update{k3s, grafana},
	}
	grafanaFree := grafana
	grafanaFree.Blocks = nil
	head := &model.Plan{
		Branches: []model.Branch{
			{Name: "pinup/k3s", Title: "update k3s", UpdateKeys: []string{k3s.Key(), image.Key()}, Labels: []string{"needs-runbook"}},
			{Name: "pinup/grafana", Title: "update grafana", UpdateKeys: []string{grafana.Key()}, Automerge: true},
			{Name: "pinup/curl", Title: "update curl", Automerge: false},
		},
		Updates:  []model.Update{k3s, image, grafanaFree},
		Warnings: []model.Warning{{Msg: "k3s-upgrade: lookup is new"}},
	}

	got := PlanDiff(base, head)
	for _, want := range []string{
		"**New merge requests** (2)", "`pinup/k3s` — update k3s", "`pinup/grafana` — update grafana (automerge)",
		"**No longer proposed** (1)", "`pinup/k3s-io-k3s-1.x` — update k3s-io/k3s to v1.37.0+k3s1",
		"**Changed merge requests** (1)", "`pinup/curl` — automerge true → false",
		"**No longer held** (1)", "`monitoring-grafana` 2.3.19 → 2.3.21",
		"**New warnings** (1)", "k3s-upgrade: lookup is new",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff lacks %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "No difference") {
		t.Errorf("a diff with changes claims none\n%s", got)
	}

	// And the other way round: the grafana update becomes held again, with
	// the reason and the rule that holds it.
	back := PlanDiff(head, base)
	for _, want := range []string{"**Newly held** (1)", "concurrentLimit: prConcurrentLimit 10", "(held by config)", "now held: concurrentLimit"} {
		if !strings.Contains(back, want) {
			t.Errorf("reverse diff lacks %q\n%s", want, back)
		}
	}
}

func TestPlanDiffOfTheSamePlanSaysSo(t *testing.T) {
	p := &model.Plan{Branches: []model.Branch{{Name: "pinup/a", Title: "update a", Automerge: true}}}
	if got := PlanDiff(p, p); !strings.Contains(got, "No difference") {
		t.Errorf("identical plans:\n%s", got)
	}
}
