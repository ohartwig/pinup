// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"git.ole-hartwig.eu/pinup/pinup/hbs"
	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/sched"
	"git.ole-hartwig.eu/pinup/pinup/versioning"
)

// The templates that name a branch and title its merge request live in the
// resolved configuration - branchName, branchTopic, commitMessage and their
// per-update-type overrides under digest, pin, group and the rest - and
// were captured by execution with everything else. This file renders them.
// What it adds is measured against the estate's live renovate/* branches
// (testdata/parity/live/renovate-branches.json): the manager-level commit
// topics Renovate keeps outside the configuration, the "(major)" group
// split, and the cleaning a branch name goes through.

// ManagerTopic is the commitMessageTopic a manager supplies when the
// configuration names none. Measured from merge-request titles: "update
// registry.../code-signing docker tag to v1.3.10" for an image, "update
// dependency devops/ci-cd-components/... to v..." for a component include
// read by the same gitlabci manager, "update module
// github.com/aws/aws-sdk-go-v2 to v1.47.0" for gomod.
func ManagerTopic(manager, datasource string) string {
	switch manager {
	case "dockerfile", "gitlabci", "docker-compose", "kustomize", "helm-values":
		if datasource == "docker" {
			return "{{{depName}}} Docker tag"
		}
	case "gomod":
		return "module {{depName}}"
	}
	return ""
}

// updateTypeKey is the configuration object merged over an update of that
// type: config.digest for a digest update, config.major for a major.
func updateTypeKey(t model.UpdateType) string {
	switch t {
	case model.UpdateMajor:
		return "major"
	case model.UpdateMinor:
		return "minor"
	case model.UpdatePatch:
		return "patch"
	case model.UpdateDigest:
		return "digest"
	case model.UpdatePin:
		return "pin"
	case model.UpdatePinDigest:
		return "pinDigest"
	case model.UpdateRollback:
		return "rollback"
	case model.UpdateReplacement:
		return "replacement"
	case model.UpdateLockFileMaintenance:
		return "lockFileMaintenance"
	}
	return ""
}

// Named is one update with its branch name and messages rendered.
type Named struct {
	Update model.Update
	Branch string
	// Title is the message for this update on its own. GroupTitle is the
	// message when it shares a branch with others, taking the extra ("to
	// v4") the members agree on, or none.
	Title      string
	GroupTitle func(extra string) string
	// Extra is the rendered commitMessageExtra of this update, for the
	// group to compare.
	Extra     string
	GroupName string
	GroupSlug string
	Automerge bool
	Labels    []string
	Schedule  []string
	Timezone  string
}

// Name renders the branch name and commit message for one update from the
// configuration the rules produced for it. vs is used to read the new
// version's components for the template variables.
func Name(u model.Update, cfg map[string]any, vs versioning.Registry) (Named, error) {
	cfg = overlay(cfg, updateTypeKey(u.Type))

	// The configuration before the group overlay names a single update;
	// measured, a group of one is titled like the update itself -
	// "renovate/major-typo3-core" carries "update dependency typo3/cms-core
	// to v14", not "update TYPO3 Core (major)".
	single := cfg
	groupName, _ := cfg["groupName"].(string)
	groupSlug, _ := cfg["groupSlug"].(string)
	grouped := groupName != ""
	majorGroup := false
	if grouped {
		cfg = overlay(cfg, "group")
		if groupSlug == "" {
			groupSlug = Slugify(groupName)
		}
		// A group's majors travel on their own branch. Measured:
		// "renovate/major-hocuspocus-packages", "renovate/major-laravel".
		if sep, _ := cfg["separateMajorMinor"].(bool); sep && u.Type == model.UpdateMajor {
			majorGroup = true
			groupSlug = "major-" + groupSlug
		}
	}

	env := variables(u, cfg, vs, groupName, groupSlug)
	render := func(c map[string]any, key string) (string, error) {
		tmpl, _ := c[key].(string)
		if tmpl == "" {
			return "", nil
		}
		out, _, err := hbs.RenderString(tmpl, env)
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		return out, nil
	}

	topic, err := render(cfg, "branchTopic")
	if err != nil {
		return Named{}, err
	}
	env.Values["branchTopic"] = topic
	branch, err := render(cfg, "branchName")
	if err != nil {
		return Named{}, err
	}
	branch = cleanBranchName(branch)

	// A manager's own commit topic sits between Renovate's default and
	// anything the configuration set: it applies when the topic is still
	// the builtin default.
	if topic, _ := single["commitMessageTopic"].(string); topic == "" || topic == builtinTopic {
		if mt := ManagerTopic(u.Dep.Manager, u.Dep.Datasource); mt != "" {
			single = overlay(single, "")
			single["commitMessageTopic"] = mt
		}
	}
	prefix := semanticPrefix(cfg)
	lower := true
	if lc, _ := cfg["commitMessageLowerCase"].(string); lc == "never" {
		lower = false
	}
	message := func(c map[string]any, extra *string) (string, error) {
		for _, part := range []string{"commitMessageAction", "commitMessageTopic", "commitMessageExtra", "commitMessageSuffix"} {
			v, err := render(c, part)
			if err != nil {
				return "", err
			}
			env.Values[part] = strings.TrimSpace(v)
		}
		if extra != nil {
			env.Values["commitMessageExtra"] = *extra
		}
		env.Values["commitMessagePrefix"] = prefix
		msg, err := render(c, "commitMessage")
		if err != nil {
			return "", err
		}
		msg = strings.Join(strings.Fields(msg), " ")
		if prefix != "" && lower {
			msg = lowerAfterPrefix(msg, prefix)
		}
		return msg, nil
	}
	title, err := message(single, nil)
	if err != nil {
		return Named{}, err
	}
	extra := env.Values["commitMessageExtra"]

	n := Named{Update: u, Branch: branch, Title: title, Extra: extra, GroupSlug: groupSlug}
	if grouped {
		n.GroupName = groupName
		n.GroupTitle = func(extra string) string {
			// Measured: "update hocuspocus packages to v4" when the
			// members agree on the version, "update laravel (major)" when
			// they do not. The suffix marks a major group that cannot
			// name one target.
			c := cfg
			if majorGroup && extra == "" {
				c = overlay(cfg, "")
				c["groupName"] = groupName + " (major)"
				env.Values["groupName"] = groupName + " (major)"
				defer func() { env.Values["groupName"] = groupName }()
			}
			t, err := message(c, &extra)
			if err != nil {
				return title
			}
			return t
		}
	}
	n.Automerge, _ = cfg["automerge"].(bool)
	n.Labels = append(stringsOf(cfg["labels"]), stringsOf(cfg["addLabels"])...)
	n.Schedule = stringsOf(cfg["schedule"])
	n.Timezone, _ = cfg["timezone"].(string)
	return n, nil
}

// Compose turns named updates into branches: one per branch name, with the
// updates that share it. A branch all of whose updates are held is kept,
// marked SuppressedBy and without edits - the shadow comparator needs to
// see what pinup would have pushed and why it did not - while a branch
// with at least one actionable update carries only those. A branch of one
// is titled as its update; a group is titled as the group, with the extra
// its members agree on.
func Compose(named []Named) ([]model.Branch, error) {
	type member struct {
		branch  *model.Branch
		members []Named
	}
	byName := map[string]*member{}
	var order []string
	// Actionable updates first, so a branch with any of them is built from
	// those; held ones only add a branch when nothing actionable shares it.
	ordered := append([]Named(nil), named...)
	sort.SliceStable(ordered, func(i, j int) bool { return !ordered[i].Update.Blocked() && ordered[j].Update.Blocked() })
	for _, n := range ordered {
		m, ok := byName[n.Branch]
		if ok && n.Update.Blocked() {
			if m.branch.SuppressedBy == "" {
				continue // an actionable branch; the held update stays off it
			}
		}
		if !ok {
			b := &model.Branch{
				Name: n.Branch, Slug: strings.TrimPrefix(n.Branch, "renovate/"),
				GroupName: n.GroupName, Automerge: n.Automerge, Labels: n.Labels,
			}
			if n.Update.Blocked() {
				b.SuppressedBy = n.Update.SuppressedBy
				if b.SuppressedBy == "" {
					b.SuppressedBy = n.Update.Blocks[0].Reason
				}
			}
			if len(n.Schedule) > 0 {
				s, err := sched.Parse(n.Schedule, n.Timezone)
				if err != nil {
					return nil, err
				}
				b.Schedule = model.Window{Expr: s.String(), Kind: sched.Kind(n.Schedule[0]), Timezone: s.Timezone()}
			}
			m = &member{branch: b}
			byName[n.Branch] = m
			order = append(order, n.Branch)
		}
		m.branch.UpdateKeys = append(m.branch.UpdateKeys, n.Update.DepKey)
		// A group merges automatically only if every member may.
		m.branch.Automerge = m.branch.Automerge && n.Automerge
		m.members = append(m.members, n)
	}
	out := make([]model.Branch, 0, len(order))
	for _, name := range order {
		m := byName[name]
		first := m.members[0]
		// Measured: "renovate/ci-components" on moselwal-websites-deploy is
		// titled "update dependency devops/ci-cd-components/deploy-tools
		// to ..." - one dependency, read by two managers, is one update
		// for the title's purposes.
		distinct := map[string]bool{}
		for _, n := range m.members {
			distinct[n.Update.Dep.DepName+"\x00"+n.Update.NewValue] = true
		}
		switch {
		case len(distinct) == 1 || first.GroupTitle == nil:
			m.branch.Title = first.Title
		default:
			shared := first.Extra
			for _, n := range m.members[1:] {
				if n.Extra != shared {
					shared = ""
					break
				}
			}
			m.branch.Title = first.GroupTitle(shared)
		}
		out = append(out, *m.branch)
	}
	return out, nil
}

// Overlay merges the configuration object for an update type - config.digest
// for a digest update, config.lockFileMaintenance for a lock refresh - over
// a copy of cfg, the way Renovate applies per-update-type settings. The
// policy and the branch naming both read the result.
func Overlay(cfg map[string]any, t model.UpdateType) map[string]any {
	return overlay(cfg, updateTypeKey(t))
}

// overlay merges cfg[key], when it is an object, over a copy of cfg -
// Renovate's per-update-type and group configuration. Objects merge one
// level deep; that is as deep as these objects go.
func overlay(cfg map[string]any, key string) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if key == "" {
		return out
	}
	obj, ok := cfg[key].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range obj {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := out[k].(map[string]any); ok {
				merged := make(map[string]any, len(existing)+len(sub))
				for kk, vv := range existing {
					merged[kk] = vv
				}
				for kk, vv := range sub {
					merged[kk] = vv
				}
				out[k] = merged
				continue
			}
		}
		out[k] = v
	}
	return out
}

func variables(u model.Update, cfg map[string]any, vs versioning.Registry, groupName, groupSlug string) hbs.MapEnv {
	env := hbs.MapEnv{Values: map[string]string{}, Absent: map[string]bool{}}
	set := func(k, v string) {
		if v == "" {
			env.Absent[k] = true
			return
		}
		env.Values[k] = v
	}
	d := u.Dep
	set("depName", d.DepName)
	set("packageName", d.PackageName)
	set("depNameSanitized", sanitizeDepName(d.DepName))
	set("datasource", d.Datasource)
	set("manager", d.Manager)
	set("currentValue", d.CurrentValue)
	set("currentDigest", d.CurrentDigest)
	set("newValue", u.NewValue)
	set("newVersion", u.NewVersion)
	set("newDigest", u.NewDigest)
	if len(u.NewDigest) > 7 {
		short := u.NewDigest
		if i := strings.IndexByte(short, ':'); i >= 0 {
			short = short[i+1:]
		}
		set("newDigestShort", short[:7])
	}
	set("groupName", groupName)
	set("groupSlug", groupSlug)
	for k, v := range map[string]any{
		"branchPrefix": cfg["branchPrefix"], "additionalBranchPrefix": cfg["additionalBranchPrefix"],
		"separateMinorPatch": cfg["separateMinorPatch"], "separateMultipleMinor": cfg["separateMultipleMinor"],
		"commitMessageSuffix": cfg["commitMessageSuffix"],
	} {
		switch x := v.(type) {
		case string:
			set(k, x)
		case bool:
			if x {
				set(k, "true")
			}
		}
	}
	flags := map[string]bool{
		"isMajor": u.Type == model.UpdateMajor, "isMinor": u.Type == model.UpdateMinor, "isPatch": u.Type == model.UpdatePatch,
		"isDigest": u.Type == model.UpdateDigest, "isPin": u.Type == model.UpdatePin, "isPinDigest": u.Type == model.UpdatePinDigest,
		"isLockfileUpdate": u.Type == model.UpdateLockFileMaintenance, "isReplacement": u.Type == model.UpdateReplacement,
	}
	// isSingleVersion is whether the new value is one version rather than
	// a range - "3.11.3", not "^8.5.10".
	scheme, schemeErr := vs.Get(firstNonEmpty(d.Versioning, "semver"))
	if schemeErr == nil && u.NewValue != "" && scheme.IsVersion(u.NewValue) {
		flags["isSingleVersion"] = true
	}
	for k, v := range flags {
		if v {
			set(k, "true")
		}
	}
	if schemeErr == nil && u.NewVersion != "" {
		if maj, ok := scheme.Major(u.NewVersion); ok {
			set("newMajor", strconv.Itoa(maj))
			set("prettyNewMajor", "v"+strconv.Itoa(maj))
		}
		if min, ok := scheme.Minor(u.NewVersion); ok {
			set("newMinor", strconv.Itoa(min))
		}
	}
	if u.NewVersion != "" {
		pretty := u.NewVersion
		if !strings.HasPrefix(pretty, "v") && pretty != "" && unicode.IsDigit(rune(pretty[0])) {
			pretty = "v" + pretty
		}
		set("prettyNewVersion", pretty)
	}
	return env
}

// sanitizeDepName is Renovate's depNameSanitized, measured on the estate:
// "golang.org/x/mobile" -> "golang.org-x-mobile", "@scope/pkg" ->
// "scope-pkg", "https://github.com/crowdsecurity/hub" ->
// "https-github.com-crowdsecurity-hub".
func sanitizeDepName(name string) string {
	s := strings.ReplaceAll(name, "@", "")
	s = strings.ReplaceAll(s, "://", "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ":", "-")
	return s
}

var branchUnsafe = regexp.MustCompile(`[^A-Za-z0-9/._-]+`)

// cleanBranchName is what a rendered name goes through before git sees it:
// runs of characters git refuses or that read badly become one dash, and a
// dash never leads or trails a path segment.
func cleanBranchName(s string) string {
	s = branchUnsafe.ReplaceAllString(s, "-")
	parts := strings.Split(s, "/")
	for i := range parts {
		parts[i] = strings.Trim(parts[i], "-.")
	}
	return strings.Join(parts, "/")
}

// Slugify is the slug of a group name: lowercase, runs of anything but
// letters and digits become one dash. "Pin Dependencies" ->
// "pin-dependencies", "hocuspocus packages" -> "hocuspocus-packages".
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// semanticPrefix is "chore(deps):" - the configured type and scope - when
// semantic commits are on or auto, else the configured prefix.
func semanticPrefix(cfg map[string]any) string {
	if p, ok := cfg["commitMessagePrefix"].(string); ok && p != "" {
		return p
	}
	mode, _ := cfg["semanticCommits"].(string)
	if mode == "disabled" {
		return ""
	}
	typ, _ := cfg["semanticCommitType"].(string)
	if typ == "" {
		typ = "chore"
	}
	scope, _ := cfg["semanticCommitScope"].(string)
	if scope != "" {
		return typ + "(" + scope + "):"
	}
	return typ + ":"
}

// lowerAfterPrefix lowercases everything after the semantic prefix.
// Measured: "update ci components" from groupName "CI components", and
// "[security]" from the "[SECURITY]" suffix.
func lowerAfterPrefix(msg, prefix string) string {
	rest := strings.TrimLeft(strings.TrimPrefix(msg, prefix), " ")
	if rest == "" {
		return msg
	}
	return prefix + " " + strings.ToLower(rest)
}

// builtinTopic is Renovate's default commitMessageTopic, the one a
// manager's own topic replaces.
const builtinTopic = "dependency {{depName}}"

func stringsOf(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
