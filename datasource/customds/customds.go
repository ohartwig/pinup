// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

// Package customds implements Renovate's customDatasources: a URL rendered
// from a template, a document in JSON or plain-text form, and JSONata
// transforms that shape it into {"releases": [{"version": ...}]}.
//
// The estate declares three. Two of them, wolfi and koh-apk, point at the
// Renovate runner's local sidecar and are served natively by apkds instead;
// wire registers those names there and this package never sees them. The
// third, protonpass, is what this package is for.
package customds

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.ole-hartwig.eu/pinup/pinup/hbs"
	"git.ole-hartwig.eu/pinup/pinup/httpx"
	"git.ole-hartwig.eu/pinup/pinup/jsonata"
	"git.ole-hartwig.eu/pinup/pinup/lookup"
	"git.ole-hartwig.eu/pinup/pinup/model"
)

// Datasource serves one customDatasources entry as "custom.<name>".
type Datasource struct {
	def    model.CustomDatasource
	client *httpx.Client
}

// New returns the datasource for a definition.
func New(def model.CustomDatasource, client *httpx.Client) *Datasource {
	return &Datasource{def: def, client: client}
}

func (d *Datasource) Name() string { return "custom." + d.def.Name }

// DefaultVersioning is semver, as Renovate's is for custom datasources.
func (d *Datasource) DefaultVersioning() string { return "semver" }

// Releases fetches, transforms and reads the releases. A registry URL on
// the dependency overrides the template.
func (d *Datasource) Releases(ctx context.Context, ref lookup.Ref) (*model.ReleaseSet, error) {
	url := ""
	if len(ref.RegistryURLs) > 0 && ref.RegistryURLs[0] != "" {
		url = ref.RegistryURLs[0]
	} else {
		var set bool
		var err error
		url, set, err = hbs.RenderString(d.def.DefaultRegistryURLTemplate, hbs.MapEnv{Values: map[string]string{"packageName": ref.PackageName}})
		if err != nil {
			return nil, fmt.Errorf("%s: defaultRegistryUrlTemplate: %w", d.Name(), err)
		}
		if !set || url == "" {
			return nil, fmt.Errorf("%s: no registry URL for %s", d.Name(), ref.PackageName)
		}
	}
	resp, err := d.client.Get(ctx, url, httpx.ReqOptions{Accept: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", d.Name(), ref.PackageName, err)
	}

	var doc any
	switch strings.ToLower(d.def.Format) {
	case "", "json":
		if err := json.Unmarshal(resp.Body, &doc); err != nil {
			return nil, fmt.Errorf("%s: %s: %s is not JSON: %w", d.Name(), ref.PackageName, url, err)
		}
	case "plain":
		// One version per line, blank lines and # comments dropped, as
		// Renovate reads a plain document.
		var releases []any
		for _, line := range strings.Split(string(resp.Body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			releases = append(releases, map[string]any{"version": line})
		}
		doc = map[string]any{"releases": releases}
	default:
		return nil, fmt.Errorf("%s: format %q is not supported (json, plain)", d.Name(), d.def.Format)
	}

	for _, tmpl := range d.def.TransformTemplates {
		doc, err = jsonata.Transform(tmpl, doc)
		if err != nil {
			return nil, fmt.Errorf("%s: transformTemplates %q: %w", d.Name(), tmpl, err)
		}
	}
	return d.readReleases(ref, doc)
}

// readReleases reads Renovate's release document shape: releases[] with
// version, optionally releaseTimestamp, isDeprecated, sourceUrl; plus a
// top-level sourceUrl and homepage.
func (d *Datasource) readReleases(ref lookup.Ref, doc any) (*model.ReleaseSet, error) {
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s: the transformed document is %T, not an object with releases", d.Name(), ref.PackageName, doc)
	}
	list, ok := obj["releases"].([]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s: the transformed document has no releases list", d.Name(), ref.PackageName)
	}
	rs := &model.ReleaseSet{PackageName: ref.PackageName, Datasource: d.Name()}
	if s, ok := obj["sourceUrl"].(string); ok {
		rs.SourceURL = s
	}
	for i, item := range list {
		rel, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: %s: releases[%d] is %T, not an object", d.Name(), ref.PackageName, i, item)
		}
		version, _ := rel["version"].(string)
		if version == "" {
			continue
		}
		r := model.Release{Version: version}
		if ts, ok := rel["releaseTimestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				r.Timestamp = t
			}
		}
		if dep, ok := rel["isDeprecated"].(bool); ok {
			r.Deprecated = dep
		}
		if s, ok := rel["sourceUrl"].(string); ok {
			r.SourceURL = s
		}
		rs.Releases = append(rs.Releases, r)
	}
	return rs, nil
}
