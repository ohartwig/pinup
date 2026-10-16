// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ohartwig/pinup/config/preset"
	"github.com/ohartwig/pinup/model"
)

// Decoded is the part of a resolved configuration the pipeline needs in typed
// form.
//
// It is deliberately a subset. The merged document carries several hundred
// keys after preset expansion, most of which belong to stages that do not
// exist yet; decoding all of them now would mean inventing shapes for
// behaviour nobody has written, and every one of those guesses would have to
// be revisited.
type Decoded struct {
	// CustomDatasources is `customDatasources`, keyed by name; a dependency
	// names one as "custom.<name>".
	CustomDatasources map[string]model.CustomDatasource
	EnabledManagers   []string
	IgnorePaths       []string
	// FilePatterns maps a manager name to its configured patterns, including
	// one synthetic entry per custom manager.
	FilePatterns map[string][]string
	// CustomManagers are the configured regex managers, in the order the
	// configuration lists them. The index travels onto every dependency they
	// produce.
	CustomManagers []model.CustomManager
	// ArtifactManifests are the configured artifact manifests, in the order
	// the configuration lists them; each is discovered under its own key
	// (ArtifactManifestName) like a custom manager.
	ArtifactManifests []model.ArtifactManifest
	Timezone          string
	Schedule          []string
}

// ArtifactManifestName is the discovery key an artifactManifests entry is
// matched under: its files are walked with the managers' files, and the
// manifest stage - not extraction - reads what matched.
func ArtifactManifestName(index int) string {
	return fmt.Sprintf("artifact#%d", index)
}

// ArtifactFormats are the manifest formats the sbom package reads.
var ArtifactFormats = []string{"cyclonedx", "spdx", "trivy", "go-modules"}

// CustomManagerName is the manager key a custom definition is discovered
// under. Each definition gets its own key so discovery can hand the right
// definition to the manager, rather than running all thirty-seven over every
// file that any of them matches.
func CustomManagerName(index int) string {
	return fmt.Sprintf("custom.regex#%d", index)
}

// Decode reads the typed subset out of a merged document.
func Decode(raw map[string]any) (Decoded, error) {
	var d Decoded
	d.FilePatterns = map[string][]string{}

	d.EnabledManagers = stringSlice(raw["enabledManagers"])
	d.IgnorePaths = stringSlice(raw["ignorePaths"])
	d.Schedule = stringSlice(raw["schedule"])
	if tz, ok := raw["timezone"].(string); ok {
		d.Timezone = tz
	}

	list, _ := raw["customManagers"].([]any)
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return d, fmt.Errorf("customManagers[%d] is a %T, want an object", i, entry)
		}
		if t, _ := m["customType"].(string); t != "" && t != "regex" {
			// Only the regex custom type exists in this estate. Silently
			// ignoring an unknown one would drop dependencies without saying
			// so.
			return d, fmt.Errorf("customManagers[%d] has customType %q, which is not implemented", i, t)
		}
		cm := model.CustomManager{
			Index:                  i,
			MatchStrings:           stringSlice(m["matchStrings"]),
			MatchStrategy:          str(m["matchStringsStrategy"]),
			DepNameTemplate:        str(m["depNameTemplate"]),
			PackageNameTemplate:    str(m["packageNameTemplate"]),
			DatasourceTemplate:     str(m["datasourceTemplate"]),
			VersioningTemplate:     str(m["versioningTemplate"]),
			RegistryURLTemplate:    str(m["registryUrlTemplate"]),
			ExtractVersionTemplate: str(m["extractVersionTemplate"]),
			DepTypeTemplate:        str(m["depTypeTemplate"]),
			CurrentValueTemplate:   str(m["currentValueTemplate"]),
			Description:            stringSlice(m["description"]),
		}
		// managerFilePatterns is the current key; fileMatch is the name it
		// used to have. Both are read, because a repository config written
		// against the older documentation is not a reason to find nothing.
		cm.FilePatterns = stringSlice(m["managerFilePatterns"])
		if len(cm.FilePatterns) == 0 {
			cm.FilePatterns = stringSlice(m["fileMatch"])
		}
		d.CustomManagers = append(d.CustomManagers, cm)
		d.FilePatterns[CustomManagerName(i)] = cm.FilePatterns
	}

	manifests, _ := raw["artifactManifests"].([]any)
	for i, entry := range manifests {
		m, ok := entry.(map[string]any)
		if !ok {
			return d, fmt.Errorf("artifactManifests[%d] is a %T, want an object", i, entry)
		}
		am := model.ArtifactManifest{
			Index:        i,
			FilePatterns: stringSlice(m["matchFileNames"]),
			Format:       str(m["format"]),
			Path:         str(m["path"]),
			URL:          str(m["url"]),
			Datasources:  stringSlice(m["datasources"]),
			Description:  stringSlice(m["description"]),
		}
		if len(am.FilePatterns) == 0 {
			return d, fmt.Errorf("artifactManifests[%d] names no matchFileNames", i)
		}
		if !slices.Contains(ArtifactFormats, am.Format) {
			return d, fmt.Errorf("artifactManifests[%d] has format %q, want one of %s", i, am.Format, strings.Join(ArtifactFormats, ", "))
		}
		sources := 0
		if am.Path != "" {
			sources++
		}
		if am.URL != "" {
			sources++
		}
		if a, ok := m["artifact"].(map[string]any); ok {
			sources++
			am.Artifact = &model.ArtifactSource{Project: str(a["project"]), Ref: str(a["ref"]), Job: str(a["job"]), Path: str(a["path"])}
			if am.Artifact.Job == "" || am.Artifact.Path == "" {
				return d, fmt.Errorf("artifactManifests[%d].artifact needs job and path", i)
			}
		}
		if sources != 1 {
			return d, fmt.Errorf("artifactManifests[%d] must name exactly one of path, url and artifact, has %d", i, sources)
		}
		if pin, ok := m["pin"].(map[string]any); ok {
			am.Pin = &model.ArtifactPin{After: str(pin["after"]), Line: str(pin["line"]), Datasources: stringSlice(pin["datasources"])}
			if am.Pin.After == "" || am.Pin.Line == "" {
				return d, fmt.Errorf("artifactManifests[%d].pin needs after and line", i)
			}
		}
		d.ArtifactManifests = append(d.ArtifactManifests, am)
		d.FilePatterns[ArtifactManifestName(i)] = am.FilePatterns
	}

	d.CustomDatasources = map[string]model.CustomDatasource{}
	if cds, ok := raw["customDatasources"].(map[string]any); ok {
		for name, v := range cds {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			d.CustomDatasources[name] = model.CustomDatasource{
				Name:                       name,
				DefaultRegistryURLTemplate: str(m["defaultRegistryUrlTemplate"]),
				Format:                     str(m["format"]),
				TransformTemplates:         stringSlice(m["transformTemplates"]),
			}
		}
	}

	// Per-manager overrides for the built-in managers, which the config
	// expresses as a nested object keyed by manager name.
	for name, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		patterns := stringSlice(m["managerFilePatterns"])
		if len(patterns) == 0 {
			patterns = stringSlice(m["fileMatch"])
		}
		if len(patterns) > 0 {
			d.FilePatterns[name] = patterns
		}
	}
	return d, nil
}

// DecodeFile is the common case: load one file, expand its extends against
// src, and decode. A nil src resolves nothing, which is right only for a
// file with no extends - and wrong loudly otherwise, since an unexpanded
// extends is an error, not a silent no-op.
func DecodeFile(path string, src preset.Source) (Decoded, *Resolved, []string, error) {
	if src == nil {
		src = noPresets{}
	}
	r, warnings, err := ResolveFile(path, src)
	if err != nil {
		return Decoded{}, nil, nil, err
	}
	d, err := Decode(r.Raw)
	return d, r, warnings, err
}

// noPresets knows no preset at all.
type noPresets struct{}

func (noPresets) Get(string) (map[string]any, string, bool, error) { return nil, "", false, nil }

func str(v any) string {
	s, _ := v.(string)
	return s
}

// stringSlice accepts both a list and a bare string, because the config uses
// both shapes for the same keys - `description` is sometimes a string and
// sometimes an array of them.
func stringSlice(v any) []string {
	switch v := v.(type) {
	case nil:
		return nil
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		// A shape nobody expected: say so rather than returning nothing,
		// which would read as "not configured".
		b, _ := json.Marshal(v)
		panic(fmt.Sprintf("config: expected a string or a list of strings, got %T: %s", v, b))
	}
}
