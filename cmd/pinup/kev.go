// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ohartwig/pinup/kev"
	"github.com/ohartwig/pinup/lookup"
	"github.com/ohartwig/pinup/model"
)

// kevLabel marks a merge request that fixes a CVE CISA lists as exploited.
// It travels beside the configuration's labels, never instead of them.
const kevLabel = "security:kev"

// exploitedCatalog is what the run asks about known exploited
// vulnerabilities; kev.Client is the one that reads CISA's feed.
type exploitedCatalog interface {
	Load(ctx context.Context, now time.Time) (kev.Catalog, []string, error)
}

// markExploited sets Exploited on every advisory whose id or alias CISA's
// Known Exploited Vulnerabilities catalog lists - on the dependencies and
// on the transitive packages of the locks alike. The catalog is read only
// when there is an advisory to ask about, so a run without one never
// touches the feed. A catalog that cannot be read is a warning: the fixes
// are still planned, as security fixes, without the KEV priority.
func markExploited(ctx context.Context, cat exploitedCatalog, now time.Time, deps []model.Dependency, transitive map[string][]model.Advisory) []model.Warning {
	if cat == nil {
		return nil
	}
	asked := slices.ContainsFunc(deps, func(d model.Dependency) bool { return len(d.Advisories) > 0 })
	for _, found := range transitive {
		asked = asked || len(found) > 0
	}
	if !asked {
		return nil
	}
	catalog, notes, err := cat.Load(ctx, now)
	var warns []model.Warning
	for _, n := range notes {
		warns = append(warns, model.Warning{Stage: "lookup", Msg: n})
	}
	if err != nil {
		return append(warns, model.Warning{Stage: "lookup", Msg: fmt.Sprintf("known exploited vulnerabilities: %v; security fixes are planned without the KEV priority", err)})
	}
	mark := func(advisories []model.Advisory) {
		for i := range advisories {
			a := &advisories[i]
			if a.Withdrawn {
				continue
			}
			if e, ok := catalog.Match(append([]string{a.ID}, a.Aliases...)...); ok {
				a.Exploited = &model.Exploited{CVE: e.CVE, DateAdded: e.DateAdded, DueDate: e.DueDate, Ransomware: e.Ransomware}
			}
		}
	}
	for i := range deps {
		mark(deps[i].Advisories)
	}
	for _, found := range transitive {
		mark(found)
	}
	return warns
}

// kevStore keeps the catalog in the run's cache, under the release cache's
// own freshness rule.
type kevStore struct {
	cache lookup.Cache
	// warn receives a cache write failure; nil drops it.
	warn func(string)
}

func (s kevStore) Get(key string, ttl time.Duration, now time.Time) ([]byte, bool, bool) {
	payload, fresh, err := s.cache.GetReleases(key, ttl, now)
	return payload, fresh, err == nil && len(payload) > 0
}

func (s kevStore) Put(key string, payload []byte, now time.Time) {
	if err := s.cache.PutReleases(key, payload, now); err != nil && s.warn != nil {
		s.warn(fmt.Sprintf("cache: kev catalog: %v", err))
	}
}
