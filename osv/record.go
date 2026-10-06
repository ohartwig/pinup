// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package osv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ohartwig/pinup/versioning"
)

// Record is one advisory in the OSV schema, as an advisory feed outside
// OSV's API publishes it - one file of OSV's own export, or an entry of a
// private feed (PINUP_PRIVATE_ADVISORIES).
type Record struct{ doc vulnDoc }

// ID is the record's id.
func (r Record) ID() string { return r.doc.ID }

// ParseRecords reads OSV records from one document: a single record, an
// array of them, or an object with a "vulns" array.
func ParseRecords(data []byte) ([]Record, error) {
	data = bytes.TrimSpace(data)
	var docs []vulnDoc
	switch {
	case len(data) == 0:
		return nil, nil
	case data[0] == '[':
		if err := json.Unmarshal(data, &docs); err != nil {
			return nil, err
		}
	default:
		var probe struct {
			Vulns []vulnDoc `json:"vulns"`
			ID    string    `json:"id"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, err
		}
		if probe.ID == "" && probe.Vulns == nil {
			return nil, fmt.Errorf("neither an OSV record nor a list of them")
		}
		if probe.ID != "" {
			var d vulnDoc
			if err := json.Unmarshal(data, &d); err != nil {
				return nil, err
			}
			docs = []vulnDoc{d}
		} else {
			docs = probe.Vulns
		}
	}
	out := make([]Record, 0, len(docs))
	for _, d := range docs {
		if d.ID == "" {
			return nil, fmt.Errorf("an OSV record without an id")
		}
		out = append(out, Record{doc: d})
	}
	return out, nil
}

// Packages are the package keys the record names in its affected entries:
// "purl:<purl without version>" and "eco:<ecosystem>/<name>".
func (r Record) Packages() []string {
	var keys []string
	for _, a := range r.doc.Affected {
		if a.Package.Purl != "" {
			keys = append(keys, "purl:"+PurlBase(a.Package.Purl))
		}
		if a.Package.Ecosystem != "" && a.Package.Name != "" {
			keys = append(keys, "eco:"+a.Package.Ecosystem+"/"+a.Package.Name)
		}
	}
	return keys
}

// Evaluate reports whether the record affects version of the package named
// by purl (or, failing a purl on the entry, by ecosystem and name), under
// scheme - the same range walk the OSV client applies to OSV's answers.
func (r Record) Evaluate(purl, ecosystem, name, version string, scheme versioning.Versioning) (Advisory, bool) {
	doc := r.doc
	doc.Affected = nil
	base := PurlBase(purl)
	for _, a := range r.doc.Affected {
		match := (a.Package.Purl != "" && PurlBase(a.Package.Purl) == base && base != "") ||
			(a.Package.Ecosystem == ecosystem && a.Package.Name == name && name != "")
		if !match {
			continue
		}
		// evaluateAdvisory matches by ecosystem and name; the entry is
		// renamed to what is asked so a purl match counts the same.
		a.Package.Ecosystem, a.Package.Name = ecosystem, name
		doc.Affected = append(doc.Affected, a)
	}
	if len(doc.Affected) == 0 {
		return Advisory{}, false
	}
	return evaluateAdvisory(&doc, name, ecosystem, version, scheme)
}

// PurlBase is a package URL without its version, qualifiers and subpath:
// "pkg:composer/koh/x@1.0.0?x=y" is "pkg:composer/koh/x". Type and
// namespace are lower-cased, as the purl specification normalises them.
func PurlBase(purl string) string {
	p := purl
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if i := strings.LastIndex(p, "@"); i > strings.LastIndex(p, "/") {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "pkg:") {
		return ""
	}
	typ, rest, ok := strings.Cut(strings.TrimPrefix(p, "pkg:"), "/")
	if !ok {
		return ""
	}
	return "pkg:" + strings.ToLower(typ) + "/" + strings.ToLower(rest)
}
