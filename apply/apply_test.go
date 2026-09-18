// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package apply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// The file: CRLF line endings, a tab, trailing whitespace, a comment with
// odd spacing - everything a serialiser would normalise.
const src = "# keep  this   comment\r\nFROM alpine:3.20   \r\n\tARG X=1.0.0 # x\r\nFROM golang:1.26\r\n"

func edit(old, new string) model.Edit {
	i := strings.Index(src, old)
	return model.Edit{File: "Containerfile", Start: i, End: i + len(old), Old: old, New: new, Manager: "test"}
}

func TestMultiEditKeepsEveryOtherByte(t *testing.T) {
	// Edits given out of order, sizes differ (shorter and longer), so
	// offsets shift if applied naively front to back.
	out, err := Apply([]byte(src), []model.Edit{edit("1.26", "1.27.1"), edit("3.20", "3.21"), edit("1.0.0", "1.1")})
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep  this   comment\r\nFROM alpine:3.21   \r\n\tARG X=1.1 # x\r\nFROM golang:1.27.1\r\n"
	if string(out) != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestOverlapIsAConflictAndNothingIsApplied(t *testing.T) {
	a := edit("3.20", "3.21")
	b := model.Edit{File: "Containerfile", Start: a.Start + 2, End: a.End + 1, Old: "20 ", New: "x", Manager: "other"}
	cs := Check([]model.Edit{a, b})
	if len(cs) != 1 {
		t.Fatalf("want one conflict, got %d", len(cs))
	}
	if msg := cs[0].Error(); !strings.Contains(msg, "test") || !strings.Contains(msg, "other") || !strings.Contains(msg, "neither is applied") {
		t.Errorf("a conflict names both managers and says nothing was applied: %q", msg)
	}
	if _, err := Apply([]byte(src), []model.Edit{a, b}); err == nil {
		t.Error("Apply must refuse conflicting edits")
	}
	// Adjacent, not overlapping: fine.
	c := model.Edit{File: "Containerfile", Start: a.End, End: a.End + 3, Old: "   ", New: "", Manager: "other"}
	if len(Check([]model.Edit{a, c})) != 0 {
		t.Error("touching ranges do not overlap")
	}
	// Different files never conflict.
	d := a
	d.File = "other"
	if len(Check([]model.Edit{a, d})) != 0 {
		t.Error("edits in different files do not conflict")
	}
}

func TestChangedFileIsRefused(t *testing.T) {
	e := edit("3.20", "3.21")
	changed := strings.Replace(src, "3.20", "3.19", 1)
	_, err := Apply([]byte(changed), []model.Edit{e})
	if err == nil || !strings.Contains(err.Error(), "changed since the plan") {
		t.Fatalf("want a changed-file error, got %v", err)
	}
	_, err = Apply([]byte(src), []model.Edit{{File: "Containerfile", Start: 5, End: 9999, Old: "x", New: "y"}})
	if err == nil || !strings.Contains(err.Error(), "outside the file") {
		t.Fatalf("want an out-of-range error, got %v", err)
	}
}

func TestWriteFilesIsAllOrNothing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Containerfile"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("v1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// One good file, one whose edit no longer matches: nothing is written.
	_, err := WriteFiles(root, []model.Edit{
		edit("3.20", "3.21"),
		{File: "other.txt", Start: 0, End: 2, Old: "v9", New: "v2"},
	})
	if err == nil {
		t.Fatal("a stale edit must fail the write")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "Containerfile")); string(got) != src {
		t.Error("the good file was written although the write failed as a whole")
	}

	res, err := WriteFiles(root, []model.Edit{edit("3.20", "3.21"), {File: "other.txt", Start: 0, End: 2, Old: "v1", New: "v2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].File != "Containerfile" || res[0].SHA256Before == res[0].SHA256After {
		t.Errorf("results %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "other.txt")); string(got) != "v2\n" {
		t.Errorf("other.txt = %q", got)
	}
	info, _ := os.Stat(filepath.Join(root, "other.txt"))
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode changed to %v", info.Mode())
	}
}

// The same edit asked for twice - two managers on one component pin - is
// one edit, not a conflict; a different replacement on the same bytes is.
func TestIdenticalEditsAreOneEdit(t *testing.T) {
	a := edit("3.20", "3.21")
	b := a
	b.Manager = "custom.regex"
	if cs := Check([]model.Edit{a, b}); len(cs) != 0 {
		t.Fatalf("identical edits conflict: %v", cs)
	}
	out, err := Apply([]byte(src), []model.Edit{a, b})
	if err != nil || !strings.Contains(string(out), "alpine:3.21   ") {
		t.Errorf("apply of a duplicated edit: %q %v", out, err)
	}
	c := a
	c.New = "3.22"
	if cs := Check([]model.Edit{a, c}); len(cs) != 1 {
		t.Errorf("a different replacement on the same bytes must conflict, got %v", cs)
	}
}

// P1d.5: a change outside the task's scope names the path and fails the
// check - the caller discards the whole result.
func TestInScopeNamesTheOffendingPath(t *testing.T) {
	task := model.Task{Command: []string{"composer", "update"}, Dir: "app", FileFilters: []string{"composer.json", "composer.lock"}}
	if err := InScope(task, []string{"app/composer.lock", "app/composer.json"}); err != nil {
		t.Errorf("in scope: %v", err)
	}
	err := InScope(task, []string{"app/composer.lock", "app/vendor/autoload.php"})
	if err == nil || !strings.Contains(err.Error(), "app/vendor/autoload.php") {
		t.Errorf("out of scope: %v", err)
	}
	if err := InScope(task, []string{"composer.lock"}); err == nil || !strings.Contains(err.Error(), "outside its directory") {
		t.Errorf("outside the task directory: %v", err)
	}
	root := model.Task{Command: []string{"node", "x"}, FileFilters: []string{"**/Containerfile"}}
	if err := InScope(root, []string{"images/a/Containerfile", "Containerfile"}); err != nil {
		t.Errorf("globstar scope: %v", err)
	}
	if err := InScope(model.Task{Command: []string{"x"}}, []string{"a"}); err == nil {
		t.Error("a task without filters may change nothing")
	}
}
