// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package advise

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/model"
)

// The security checks find what widens the blast radius of an update the
// author did not look at: an automerge that covers majors, an analyzer's
// word trusted over the version string, a registry over plaintext, the
// advisory feed switched off.
var secChecks = []Check{
	{ID: "sec/automerge-major", Run: automergeMajor},
	{ID: "sec/trust-effective", Run: trustEffective},
	{ID: "sec/match-effective-without-trust", Run: matchEffectiveWithoutTrust},
	{ID: "sec/registry-http", Run: registryHTTP},
	{ID: "sec/vulnerability-alerts-off", Run: vulnerabilityAlertsOff},
	{ID: "sec/pin-digests-off", Run: pinDigestsOff},
	{ID: "sec/post-upgrade-tasks-unallowed", Run: postUpgradeTasksUnallowed},
	{ID: "sec/allowed-commands-catch-all", Run: allowedCommandsCatchAll},
	{ID: "sec/minimum-release-age-unset", Run: minimumReleaseAgeUnset},
	{ID: "sec/ignore-unstable-false", Run: ignoreUnstableFalse},
}

// safeUpdateTypes is what an automerge rule without matchUpdateTypes is
// offered instead of "everything".
var safeUpdateTypes = []any{"minor", "patch", "pin", "digest"}

// automergeMajor finds an automerge that covers major updates: a rule with
// automerge and no matchUpdateTypes, or one that lists major; and a
// top-level automerge that major.automerge does not switch off.
func automergeMajor(in *Input) []Finding {
	var out []Finding
	for i, rule := range in.resolvedRules() {
		if rule["automerge"] != true {
			continue
		}
		types, listed := rule["matchUpdateTypes"]
		list := stringsOf(types)
		if listed && !slices.Contains(list, "major") {
			continue
		}
		p := ptr("packageRules", i, "automerge")
		f := Finding{ID: "sec/automerge-major", Category: Security, Severity: Error, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("packageRules[%d] automerges major updates", i)}
		if j, own := in.ownIndex("packageRules", i); own {
			fp := ptr("packageRules", j, "matchUpdateTypes")
			switch {
			case !listed:
				f.Fix = &Fix{Pointer: fp, Op: OpSet, Value: safeUpdateTypes, Changes: []string{fp}}
			case len(list) > 1:
				var kept []any
				for _, t := range list {
					if t != "major" {
						kept = append(kept, t)
					}
				}
				f.Fix = &Fix{Pointer: fp, Op: OpSet, Value: kept, Changes: []string{fp}}
			default:
				f.Msg += "; it lists major alone, so this is deliberate"
				f.Severity = Warn
			}
		}
		out = append(out, f)
	}
	if in.Resolved.Raw["automerge"] == true {
		major, _ := in.Resolved.Raw["major"].(map[string]any)
		if major["automerge"] != false {
			f := Finding{ID: "sec/automerge-major", Category: Security, Severity: Error, Pointer: "/automerge", Frame: FrameResolved, Origin: in.originAt("/automerge"),
				Msg: "automerge is on for every update type, major included"}
			if own, ok := in.Layer.Raw["major"].(map[string]any); ok && own != nil {
				f.Fix = &Fix{Pointer: "/major/automerge", Op: OpSet, Value: false, Changes: []string{"/major/automerge"}}
			} else {
				f.Fix = &Fix{Pointer: "/major", Op: OpSet, Value: map[string]any{"automerge": false}, Changes: []string{"/major"}}
			}
			out = append(out, f)
		}
	}
	return out
}

// trustEffective finds a rule that lets the analyzer's label relax an
// automerge. The invariant is that the stricter of two labels wins;
// trustEffective is the one way to say otherwise, and it deserves a look.
func trustEffective(in *Input) []Finding {
	var out []Finding
	for i, rule := range in.resolvedRules() {
		if rule["trustEffective"] != true {
			continue
		}
		p := ptr("packageRules", i, "trustEffective")
		out = append(out, Finding{ID: "sec/trust-effective", Category: Security, Severity: Warn, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("packageRules[%d] lets the analyzer's label relax the automerge decision", i)})
	}
	return out
}

// matchEffectiveWithoutTrust finds a rule that fires on the analyzer's
// label and automerges, without trustEffective: the stricter label still
// decides, so the rule may not do what it reads as doing.
func matchEffectiveWithoutTrust(in *Input) []Finding {
	var out []Finding
	for i, rule := range in.resolvedRules() {
		if _, ok := rule["matchEffective"]; !ok || rule["automerge"] != true || rule["trustEffective"] == true {
			continue
		}
		p := ptr("packageRules", i, "matchEffective")
		out = append(out, Finding{ID: "sec/match-effective-without-trust", Category: Security, Severity: Info, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("packageRules[%d] fires on the analyzer's label, but without trustEffective the stricter label still decides the automerge", i)})
	}
	return out
}

// registryHTTP finds a registry reached over plaintext.
func registryHTTP(in *Input) []Finding {
	var out []Finding
	report := func(pointer, raw string) {
		if !plaintext(raw) {
			return
		}
		f := Finding{ID: "sec/registry-http", Category: Security, Severity: Warn, Pointer: pointer, Frame: FrameResolved, Origin: in.originAt(pointer),
			Msg: fmt.Sprintf("registry %s is reached over plaintext", raw)}
		if fp, ok := in.filePointer(pointer); ok {
			f.Fix = &Fix{Pointer: fp, Op: OpSet, Value: "https://" + strings.TrimPrefix(raw, "http://"), Changes: []string{fp}}
		}
		out = append(out, f)
	}
	for j, u := range stringsOf(in.Resolved.Raw["registryUrls"]) {
		report(ptr("registryUrls", j), u)
	}
	for i, rule := range in.resolvedRules() {
		for j, u := range stringsOf(rule["registryUrls"]) {
			report(ptr("packageRules", i, "registryUrls", j), u)
		}
	}
	if defs, ok := in.Resolved.Raw["customDatasources"].(map[string]any); ok {
		for name, v := range defs {
			if def, ok := v.(map[string]any); ok {
				if t, ok := def["defaultRegistryUrlTemplate"].(string); ok {
					report(ptr("customDatasources", name, "defaultRegistryUrlTemplate"), t)
				}
			}
		}
	}
	for i, cm := range in.Decoded.CustomManagers {
		if cm.RegistryURLTemplate != "" {
			report(ptr("customManagers", i, "registryUrlTemplate"), cm.RegistryURLTemplate)
		}
	}
	return out
}

// plaintext reports an http:// URL to a host that is not the machine itself.
func plaintext(raw string) bool {
	if !strings.HasPrefix(raw, "http://") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := u.Hostname()
	return host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasPrefix(host, "127.")
}

// vulnerabilityAlertsOff finds the advisory feed switched off or never on.
func vulnerabilityAlertsOff(in *Input) []Finding {
	if disabled(in) {
		return nil
	}
	var out []Finding
	if in.Resolved.Raw["osvVulnerabilityAlerts"] != true {
		sev := Info
		msg := "osvVulnerabilityAlerts is not on; security advisories are not consulted"
		if in.Layer.Raw["osvVulnerabilityAlerts"] == false {
			sev = Warn
			msg = "osvVulnerabilityAlerts is switched off; security advisories are not consulted"
		}
		out = append(out, Finding{ID: "sec/vulnerability-alerts-off", Category: Security, Severity: sev, Pointer: "/osvVulnerabilityAlerts", Frame: FrameResolved, Origin: in.originAt("/osvVulnerabilityAlerts"),
			Msg: msg, Fix: &Fix{Pointer: "/osvVulnerabilityAlerts", Op: OpSet, Value: true, Changes: []string{"/osvVulnerabilityAlerts"}}})
	}
	if va, ok := in.Resolved.Raw["vulnerabilityAlerts"].(map[string]any); ok && va["enabled"] == false {
		f := Finding{ID: "sec/vulnerability-alerts-off", Category: Security, Severity: Warn, Pointer: "/vulnerabilityAlerts/enabled", Frame: FrameResolved, Origin: in.originAt("/vulnerabilityAlerts"),
			Msg: "vulnerabilityAlerts is disabled; an advisory opens no fast-path branch"}
		if own, ok := in.Layer.Raw["vulnerabilityAlerts"].(map[string]any); ok && own != nil {
			f.Fix = &Fix{Pointer: "/vulnerabilityAlerts/enabled", Op: OpSet, Value: true, Changes: []string{"/vulnerabilityAlerts/enabled"}}
		} else {
			f.Fix = &Fix{Pointer: "/vulnerabilityAlerts", Op: OpSet, Value: map[string]any{"enabled": true}, Changes: []string{"/vulnerabilityAlerts"}}
		}
		out = append(out, f)
	}
	return out
}

// dockerManagers are the managers whose dependencies are container images.
var dockerManagers = []string{"dockerfile", "docker-compose", "gitlabci", "helm-values", "kubernetes", "kustomize"}

// pinDigestsOff finds container images in use and no rule pinning them by
// digest: a tag is a moving target, a digest is not.
func pinDigestsOff(in *Input) []Finding {
	if disabled(in) || in.Resolved.Raw["pinDigests"] == true {
		return nil
	}
	for _, rule := range in.resolvedRules() {
		if rule["pinDigests"] == true && slices.Contains(stringsOf(rule["matchDatasources"]), "docker") {
			return nil
		}
	}
	docker := false
	for _, m := range in.Decoded.EnabledManagers {
		if slices.Contains(dockerManagers, m) {
			docker = true
		}
	}
	for _, cm := range in.Decoded.CustomManagers {
		if cm.DatasourceTemplate == "docker" {
			docker = true
		}
	}
	for _, rule := range in.ownRules() {
		if slices.Contains(stringsOf(rule["matchDatasources"]), "docker") {
			docker = true
		}
	}
	if !docker {
		return nil
	}
	f := Finding{ID: "sec/pin-digests-off", Category: Security, Severity: Info, Pointer: "/pinDigests", Frame: FrameResolved, Origin: in.originAt("/pinDigests"),
		Msg: "container images are not pinned by digest; a tag is a moving target"}
	if _, ok := in.Layer.Raw["extends"].([]any); ok {
		f.Fix = &Fix{Pointer: "/extends", Op: OpAppend, Value: "docker:pinDigests", Changes: concatChanged}
	} else {
		f.Fix = &Fix{Pointer: "/extends", Op: OpSet, Value: []any{"docker:pinDigests"}, Changes: concatChanged}
	}
	return []Finding{f}
}

// postUpgradeTasksUnallowed finds tasks the file itself names that the
// configuration's allowlist does not admit. The runner may allow them from
// its environment; if it does not, the run holds the branch and names the
// command, every time. A preset's tasks are the preset's to allow.
func postUpgradeTasksUnallowed(in *Input) []Finding {
	if len(stringsOf(in.Resolved.Raw["allowedCommands"])) > 0 {
		return nil
	}
	var out []Finding
	report := func(pointer string, tasks any, rule int) {
		obj, ok := tasks.(map[string]any)
		if !ok || len(stringsOf(obj["commands"])) == 0 {
			return
		}
		out = append(out, Finding{ID: "sec/post-upgrade-tasks-unallowed", Category: Security, Severity: Info, Pointer: pointer, Frame: FrameFile, Origin: in.fileOrigin(pointer, rule),
			Msg: "postUpgradeTasks names commands the configuration's allowedCommands does not admit; unless the runner's PINUP_ALLOWED_COMMANDS does, the branch is held with the command named"})
	}
	report("/postUpgradeTasks", in.Layer.Raw["postUpgradeTasks"], model.NoRule)
	for i, rule := range in.ownRules() {
		report(ptr("packageRules", i, "postUpgradeTasks"), rule["postUpgradeTasks"], in.base("packageRules")+i)
	}
	return out
}

// disabled reports a configuration that switches the repository off: what
// it would do about advisories, release ages or digests is moot.
func disabled(in *Input) bool { return in.Resolved.Raw["enabled"] == false }

// allowedCommandsCatchAll finds an allowlist that allows everything.
func allowedCommandsCatchAll(in *Input) []Finding {
	var out []Finding
	for j, c := range stringsOf(in.Resolved.Raw["allowedCommands"]) {
		switch c {
		case ".*", "^.*$", ".+", "^.+$", "^.*", ".*$":
		default:
			continue
		}
		p := ptr("allowedCommands", j)
		out = append(out, Finding{ID: "sec/allowed-commands-catch-all", Category: Security, Severity: Error, Pointer: p, Frame: FrameResolved, Origin: in.originAt(p),
			Msg: fmt.Sprintf("allowedCommands[%d] admits every command", j)})
	}
	return out
}

// minimumReleaseAgeUnset finds a configuration that adopts a release the
// hour it appears, everywhere.
func minimumReleaseAgeUnset(in *Input) []Finding {
	if disabled(in) {
		return nil
	}
	if age, _ := in.Resolved.Raw["minimumReleaseAge"].(string); age != "" {
		return nil
	}
	for _, rule := range in.resolvedRules() {
		if age, _ := rule["minimumReleaseAge"].(string); age != "" {
			return nil
		}
	}
	return []Finding{{ID: "sec/minimum-release-age-unset", Category: Security, Severity: Info, Pointer: "/minimumReleaseAge", Frame: FrameResolved, Origin: in.originAt("/minimumReleaseAge"),
		Msg: "no minimumReleaseAge anywhere; a release is adopted the hour it appears",
		Fix: &Fix{Pointer: "/minimumReleaseAge", Op: OpSet, Value: "3 days", Changes: []string{"/minimumReleaseAge"}}}}
}

// ignoreUnstableFalse finds prereleases let in wholesale.
func ignoreUnstableFalse(in *Input) []Finding {
	var out []Finding
	if in.Resolved.Raw["ignoreUnstable"] == false {
		out = append(out, Finding{ID: "sec/ignore-unstable-false", Category: Security, Severity: Warn, Pointer: "/ignoreUnstable", Frame: FrameResolved, Origin: in.originAt("/ignoreUnstable"),
			Msg: "ignoreUnstable is off for every dependency; prereleases are proposed", Fix: &Fix{Pointer: "/ignoreUnstable", Op: OpSet, Value: true, Changes: []string{"/ignoreUnstable"}}})
	}
	for i, rule := range in.ownRules() {
		if rule["ignoreUnstable"] != false {
			continue
		}
		if _, ok := rule["matchPackageNames"]; ok {
			continue
		}
		if _, ok := rule["matchDepNames"]; ok {
			continue
		}
		p := ptr("packageRules", i, "ignoreUnstable")
		out = append(out, Finding{ID: "sec/ignore-unstable-false", Category: Security, Severity: Warn, Pointer: p, Frame: FrameFile, Origin: in.fileOrigin(p, in.base("packageRules")+i),
			Msg: fmt.Sprintf("packageRules[%d] lets prereleases in without naming a package", i)})
	}
	return out
}
