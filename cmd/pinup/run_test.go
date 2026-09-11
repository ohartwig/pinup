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
