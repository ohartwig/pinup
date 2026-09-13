// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: MIT

package versioning

import (
	"strings"
	"testing"

	"github.com/ohartwig/pinup/model"
)

// stub is a minimal three-part scheme, enough to exercise the free functions
// without depending on an implementation package - which would invert the
// layering this package sits above.
type stub struct{ invalid map[string]bool }

func (s stub) Name() string            { return "stub" }
func (s stub) IsVersion(v string) bool { return s.IsValid(v) }
func (s stub) IsValid(v string) bool {
	return !s.invalid[v] && len(strings.Split(v, ".")) == 3
}
func (s stub) IsStable(v string) bool { return s.IsValid(v) }
func (s stub) part(v string, i int) (int, bool) {
	if !s.IsValid(v) {
		return 0, false
	}
	p := strings.Split(v, ".")
	n := 0
	for _, c := range p[i] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
func (s stub) Major(v string) (int, bool) { return s.part(v, 0) }
func (s stub) Minor(v string) (int, bool) { return s.part(v, 1) }
func (s stub) Patch(v string) (int, bool) { return s.part(v, 2) }
func (s stub) Compare(a, b string) int {
	for i := 0; i < 3; i++ {
		x, okA := s.part(a, i)
		y, okB := s.part(b, i)
		if !okA || !okB {
			return 0
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
func (s stub) Equal(a, b string) bool                                { return a == b }
func (s stub) Satisfies(v, r string) bool                            { return v == r }
func (s stub) NewValue(_, t string, _ RangeStrategy) (string, error) { return t, nil }

func TestUpdateType(t *testing.T) {
	s := stub{}
	for _, c := range []struct {
		from, to string
		want     model.UpdateType
	}{
		{"1.0.0", "2.0.0", model.UpdateMajor},
		{"1.0.0", "1.1.0", model.UpdateMinor},
		{"1.0.0", "1.0.1", model.UpdatePatch},
		{"2.0.0", "1.0.0", model.UpdateRollback},
		{"1.0.0", "1.0.0", model.UpdateUnknown},
		{"1.0.0", "nope", model.UpdateUnknown},
		{"nope", "1.0.0", model.UpdateUnknown},
	} {
		if got := UpdateType(s, c.from, c.to); got != c.want {
			t.Errorf("UpdateType(%q -> %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// A scheme may override the classification, which is how docker keeps its
// compatibility segment out of the version decision.
type classifying struct{ stub }

func (classifying) UpdateType(from, to string) model.UpdateType {
	return model.UpdateCompatibility
}

func TestClassifierOverrides(t *testing.T) {
	if got := UpdateType(classifying{}, "1.0.0", "2.0.0"); got != model.UpdateCompatibility {
		t.Errorf("a Classifier was ignored: got %v", got)
	}
}

func TestLatestPrefersTheEarlierOnATie(t *testing.T) {
	s := stub{}
	// "1.0.0" and "1.0.0" tie; with no order between them the earlier input
	// must win, because picking arbitrarily is how a compatibility family
	// gets switched by accident.
	got, ok := Latest(s, []string{"1.0.0", "2.0.0", "not-a-version", "1.5.0"})
	if !ok || got != "2.0.0" {
		t.Errorf("Latest = (%q,%v), want (\"2.0.0\", true)", got, ok)
	}
	if _, ok := Latest(s, []string{"nope", ""}); ok {
		t.Error("Latest reported a winner among only invalid versions")
	}
}

func TestSortKeepsInvalidVersionsRatherThanDroppingThem(t *testing.T) {
	s := stub{}
	got := Sort(s, []string{"2.0.0", "nope", "1.0.0"})
	if len(got) != 3 {
		t.Fatalf("Sort lost an entry: %v", got)
	}
	if got[0] != "1.0.0" || got[1] != "2.0.0" || got[2] != "nope" {
		t.Errorf("Sort = %v, want [1.0.0 2.0.0 nope]", got)
	}
}

type paramScheme struct{ stub }

func (paramScheme) WithConfig(cfg string) (Versioning, error) {
	if cfg == "" {
		return nil, errEmpty
	}
	return stub{}, nil
}

var errEmpty = &configError{}

type configError struct{}

func (*configError) Error() string { return "empty config" }

func TestRegistryResolvesParameterisedSchemes(t *testing.T) {
	r := Registry{"stub": stub{}, "regex": paramScheme{}}

	if _, err := r.Get("stub"); err != nil {
		t.Errorf("plain lookup failed: %v", err)
	}
	if _, err := r.Get(`regex:^alpine(?<major>[0-9]+)`); err != nil {
		t.Errorf("parameterised lookup failed: %v", err)
	}
	if _, err := r.Get("nosuch"); err == nil {
		t.Error("an unknown scheme resolved; it must be a named error")
	}
	if _, err := r.Get(""); err == nil {
		t.Error("an empty scheme name resolved")
	}
}
