// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package preset

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Chain asks each source in turn and answers with the first that knows the
// name. The runner composes: the alias for its own configuration first, then
// the platform for other local> presets, then the builtin library.
type Chain []Source

// Get implements Source.
func (c Chain) Get(name string) (map[string]any, string, bool, error) {
	for _, s := range c {
		def, inert, ok, err := s.Get(name)
		if err != nil {
			return nil, "", false, err
		}
		if ok {
			return def, inert, true, nil
		}
	}
	return nil, "", false, nil
}

// Aliases answers a fixed set of names with fixed documents. The runner
// registers its own configuration file under the names the estate's
// repositories extend it by - "local>devops/renovate-runner" and
// "local>devops/renovate-runner:default.json" - so the twenty renovate.json
// files resolve without a fetch and without changing a byte in them.
type Aliases map[string]map[string]any

// Get implements Source.
func (a Aliases) Get(name string) (map[string]any, string, bool, error) {
	def, ok := a[name]
	if !ok {
		return nil, "", false, nil
	}
	return def, "", true, nil
}

// FileReader reads a file from a project on the platform.
type FileReader interface {
	ReadFile(ctx context.Context, project, path, ref string) ([]byte, error)
}

// Remote resolves Renovate's local> preset form through the platform:
//
//	local>group/project              -> default.json in that project
//	local>group/project:name         -> name.json
//	local>group/project//sub/name    -> sub/name.json (measured: the last
//	                                    segment after // is the preset name,
//	                                    not a directory holding default.json)
//	local>group/project//sub/dir:name   is refused, as Renovate refuses it
//	...#ref                          -> at that branch or tag
//
// The document is parsed as JSON with comments. Anything that cannot be
// fetched is an error naming the preset: a configuration that extends
// something unreachable would otherwise run with less than it says.
type Remote struct {
	Reader FileReader
	Ctx    context.Context
}

// Get implements Source.
func (r Remote) Get(name string) (map[string]any, string, bool, error) {
	if !strings.HasPrefix(name, "local>") {
		return nil, "", false, nil
	}
	project, path, ref, err := ParseLocal(strings.TrimPrefix(name, "local>"))
	if err != nil {
		return nil, "", false, err
	}
	ctx := r.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	raw, err := r.Reader.ReadFile(ctx, project, path, ref)
	if err != nil {
		return nil, "", false, fmt.Errorf("fetching %s from %s: %w", path, project, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(stripComments(raw), &doc); err != nil {
		return nil, "", false, fmt.Errorf("%s in %s: %w", path, project, err)
	}
	return doc, "", true, nil
}

// ParseLocal splits "group/project[//dir][:name][#ref]" into the project,
// the file path and the ref.
func ParseLocal(s string) (project, path, ref string, err error) {
	if i := strings.LastIndexByte(s, '#'); i >= 0 {
		ref = s[i+1:]
		s = s[:i]
	}
	name := "default"
	dir := ""
	if i := strings.Index(s, "//"); i >= 0 {
		// Measured (parsePreset in the pinned container):
		// "local>a/b//sub/dir" is presetPath "sub", presetName "dir";
		// "local>a/b//sub/dir:name" is "prohibited sub-preset".
		sub := strings.Trim(s[i+2:], "/")
		if strings.Contains(sub, ":") {
			return "", "", "", fmt.Errorf("preset %q: a //path and a :name cannot be combined", "local>"+s)
		}
		s = s[:i]
		if j := strings.LastIndexByte(sub, '/'); j >= 0 {
			dir, name = sub[:j], sub[j+1:]
		} else {
			name = sub
		}
	} else if i := strings.LastIndexByte(s, ':'); i >= 0 {
		name = s[i+1:]
		s = s[:i]
	}
	project = strings.Trim(s, "/")
	if project == "" || !strings.Contains(project, "/") {
		return "", "", "", fmt.Errorf("preset %q: expected local>group/project", "local>"+s)
	}
	// A name that already carries its extension is used as written:
	// "local>devops/renovate-runner:default.json" is how the estate's own
	// repositories spell it.
	path = name + ".json"
	if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".json5") {
		path = name
	}
	if dir != "" {
		path = dir + "/" + path
	}
	return project, path, ref, nil
}

// stripComments removes // and /* */ comments outside strings, enough for
// the JSON-with-comments the estate's presets are written in.
func stripComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			out = append(out, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}
