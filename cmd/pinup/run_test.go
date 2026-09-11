// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatches(t *testing.T) {
	for _, c := range []struct {
		args    []string
		wantErr bool
		wantOut string
	}{
		{[]string{"version"}, false, "dev"},
		{[]string{}, false, "usage: pinup"},
		{[]string{"nonesuch"}, true, ""},
		{[]string{"whatif"}, true, ""},
	} {
		var out, errw bytes.Buffer
		err := run(c.args, &out, &errw)
		if (err != nil) != c.wantErr {
			t.Errorf("run(%v): err = %v, want error %v", c.args, err, c.wantErr)
		}
		if c.wantOut != "" && !strings.Contains(out.String(), c.wantOut) {
			t.Errorf("run(%v): output %q does not contain %q", c.args, out.String(), c.wantOut)
		}
	}
}

// TestEveryCommandIsListed keeps `usage` from drifting away from the dispatch
// table - a command that dispatches but is not listed is undiscoverable.
func TestEveryCommandIsListed(t *testing.T) {
	var out bytes.Buffer
	usage(&out)
	for _, c := range commands() {
		if !strings.Contains(out.String(), c.name) {
			t.Errorf("command %q is not listed in usage", c.name)
		}
	}
}

func TestPrintConfigExplainAndDiff(t *testing.T) {
	var out, errw strings.Builder
	err := run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--explain", "packageRules[768].enabled"}, &out, &errw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> file:") || !strings.Contains(out.String(), "default.json") {
		t.Errorf("explain must name the file as the winner:\n%s", out.String())
	}
	if !strings.Contains(errw.String(), "mergeConfidence:all-badges") {
		t.Errorf("the inert-preset warning must reach stderr, got %q", errw.String())
	}

	out.Reset()
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--explain", "nonesuch"}, &out, &errw)
	if err == nil {
		t.Error("an unset path must be an error, not silence")
	}

	// Against the direct resolution of the file - presets, no defaults -
	// every line the snapshot has is present here, and what is only here
	// is a builtin default.
	out.Reset()
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--diff", "../../testdata/parity/renovate-43.288.0/presets/default-resolved.json"}, &out, &errw)
	if err == nil {
		t.Error("the defaults make the resolution larger than the direct snapshot; the diff must say so")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		// The direct snapshot is pre-migration: its string descriptions
		// are lists here, its minimumReleaseAge "0" is null, and the
		// global-only executionTimeout is gone. Anything else the snapshot
		// has and this lacks is a real difference.
		if !strings.HasPrefix(line, "+ ") {
			continue
		}
		migrated := strings.Contains(line, ".description \"") ||
			strings.HasSuffix(line, `minimumReleaseAge "0"`) ||
			strings.HasPrefix(line, "+ executionTimeout ")
		if !migrated {
			t.Errorf("the snapshot has a line this resolution lacks: %s", line)
		}
	}
	// And the diff must be able to fail: the production-shape capture has
	// 1540 rules and differs.
	out.Reset()
	err = run([]string{"print-config", "--config", "../../testdata/parity/config/default.json",
		"--diff", "../../testdata/parity/renovate-43.288.0/presets/runner-and-repo-resolved.json"}, &out, &errw)
	if err == nil || !strings.Contains(out.String(), "packageRules[1539]") {
		t.Errorf("diff against the 1540-rule capture must fail and show the extra rules: err=%v", err)
	}
}
