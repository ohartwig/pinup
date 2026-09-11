// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package planner

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/model"
	"git.ole-hartwig.eu/pinup/pinup/sched"
)

// Policy is the part of a resolved configuration that decides whether one
// update is acted on now. It is read off the configuration the rules engine
// produced for that update; the planner knows nothing about rules, only
// about these keys and where each came from.
type Policy struct {
	Enabled           bool
	DashboardApproval bool
	MinimumReleaseAge string
	// TimestampOptional is minimumReleaseAgeBehaviour: "timestamp-optional".
	// Under the default a release whose age is unknown is held; under this
	// it is released - the estate sets it for docker, whose registries
	// publish no timestamps.
	TimestampOptional bool
	Schedule          []string
	Timezone          string
	Automerge         bool
	GroupName         string
	GroupSlug         string

	// Origins names, per key, the source that decided it. Absent keys have
	// no entry, and a block names the origin of the key that held it.
	Origins map[string]model.Origin
}

// PolicyOf reads the policy keys out of a resolved configuration. origin
// answers where a key came from; it may be nil.
func PolicyOf(cfg map[string]any, origin func(key string) model.Origin) Policy {
	p := Policy{Enabled: true, Origins: map[string]model.Origin{}}
	if origin == nil {
		origin = func(string) model.Origin { return model.Origin{Rule: model.NoRule} }
	}
	if v, ok := cfg["enabled"].(bool); ok {
		p.Enabled = v
		p.Origins["enabled"] = origin("enabled")
	}
	if v, ok := cfg["dependencyDashboardApproval"].(bool); ok {
		p.DashboardApproval = v
		p.Origins["dependencyDashboardApproval"] = origin("dependencyDashboardApproval")
	}
	if v, ok := cfg["minimumReleaseAge"].(string); ok {
		p.MinimumReleaseAge = v
		p.Origins["minimumReleaseAge"] = origin("minimumReleaseAge")
	}
	if v, ok := cfg["minimumReleaseAgeBehaviour"].(string); ok {
		p.TimestampOptional = v == "timestamp-optional"
	}
	if v, ok := cfg["schedule"].([]any); ok {
		for _, e := range v {
			if s, ok := e.(string); ok {
				p.Schedule = append(p.Schedule, s)
			}
		}
		p.Origins["schedule"] = origin("schedule")
	} else if s, ok := cfg["schedule"].(string); ok {
		p.Schedule = []string{s}
		p.Origins["schedule"] = origin("schedule")
	}
	if v, ok := cfg["timezone"].(string); ok {
		p.Timezone = v
	}
	if v, ok := cfg["automerge"].(bool); ok {
		p.Automerge = v
		p.Origins["automerge"] = origin("automerge")
	}
	if v, ok := cfg["groupName"].(string); ok {
		p.GroupName = v
	}
	if v, ok := cfg["groupSlug"].(string); ok {
		p.GroupSlug = v
	}
	return p
}

// Decide applies the policy to one update: every reason it is not acted on
// now becomes a Block with its thaw time and origin, and SuppressedBy names
// the first. An update with no blocks is one the run will act on.
//
// The order is the order a reader wants to know: an update that is disabled
// is disabled, whatever its age; one awaiting approval waits, whatever the
// clock says.
func Decide(u model.Update, p Policy, now time.Time) (model.Update, error) {
	u.Blocks = nil
	if !p.Enabled {
		u.Blocks = append(u.Blocks, model.Block{
			Reason: model.BlockDisabled, Org: p.Origins["enabled"],
			Note: "enabled: false for this update",
		})
	}
	if p.DashboardApproval {
		u.Blocks = append(u.Blocks, model.Block{
			Reason: model.BlockDashboardApproval, Org: p.Origins["dependencyDashboardApproval"],
		})
	}

	// Deliberately stricter than Renovate: a release whose datasource
	// publishes no timestamp gets its age from the moment this cache first
	// saw it, and the hold applies to that. Renovate, having no such
	// record, releases it at once under timestamp-optional. The whole
	// point of firstseen is that a tag which appeared an hour ago is an
	// hour old, whatever the registry declines to say - and a cache that is
	// nearly empty makes everything look new, which is why the runner warns
	// about one.
	age, err := ParseAge(p.MinimumReleaseAge)
	if err != nil {
		return u, fmt.Errorf("minimumReleaseAge: %w", err)
	}
	if age > 0 {
		switch {
		case u.TimeSource == model.TimeUnknown && !p.TimestampOptional:
			u.Blocks = append(u.Blocks, model.Block{
				Reason: model.BlockMinimumReleaseAge, Org: p.Origins["minimumReleaseAge"],
				Note: "the release's age is unknown and minimumReleaseAgeBehaviour is not timestamp-optional",
			})
		case u.TimeSource == model.TimeUnknown:
			// timestamp-optional: released, and the plan says on what basis.
		case now.Before(u.ReleaseTime.Add(age)):
			u.Blocks = append(u.Blocks, model.Block{
				Reason: model.BlockMinimumReleaseAge, Org: p.Origins["minimumReleaseAge"],
				Until: u.ReleaseTime.Add(age).UTC(),
				Note:  fmt.Sprintf("%s old since %s (%s), needs %s", u.TimeSource, u.ReleaseTime.UTC().Format(time.RFC3339), humanAge(now.Sub(u.ReleaseTime)), p.MinimumReleaseAge),
			})
		}
	}

	if len(p.Schedule) > 0 {
		s, err := sched.Parse(p.Schedule, p.Timezone)
		if err != nil {
			return u, err
		}
		if !s.IsActive(now) {
			u.Blocks = append(u.Blocks, model.Block{
				Reason: model.BlockSchedule, Org: p.Origins["schedule"],
				Until: s.NextOpen(now).UTC(),
				Note:  fmt.Sprintf("outside %s (%s)", s, s.Timezone()),
			})
		}
	}

	u.SuppressedBy = ""
	if len(u.Blocks) > 0 {
		u.SuppressedBy = u.Blocks[0].Reason
	}
	return u, nil
}

// ParseAge reads Renovate's duration form: "3 days", "24 hours",
// "30 minutes", "2 weeks", "1 month", "1 year", or "0". Empty and "0" mean
// no hold. Months and years are calendar-sized approximations, as they are
// in Renovate; nothing in the estate holds for that long.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0, fmt.Errorf("%q is not \"<number> <unit>\"", s)
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not \"<number> <unit>\"", s)
	}
	var unit time.Duration
	switch strings.TrimSuffix(strings.ToLower(fields[1]), "s") {
	case "minute", "min", "m":
		unit = time.Minute
	case "hour", "hr", "h":
		unit = time.Hour
	case "day", "d":
		unit = 24 * time.Hour
	case "week", "w":
		unit = 7 * 24 * time.Hour
	case "month":
		unit = 30 * 24 * time.Hour
	case "year", "yr", "y":
		unit = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("%q: unknown unit %q", s, fields[1])
	}
	return time.Duration(n * float64(unit)), nil
}

func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		return "in the future"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
