// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package npm

// Node is Renovate's node versioning: npm's reading of versions and ranges,
// with stability meaning "a release line Node.js supports long term".
// Measured (testdata/renovate/renovate-43.288.0/versioning/node.json): the two
// schemes agree on all 1005 shared rows but isStable, where 1.0.0, 2.0.0 and
// v1.2.3 are unstable and 22.19.0 and 24.8.0 are stable.
//
// Renovate reads the LTS schedule from a data file with dates; this scheme
// has no clock and calls every even major from 4 on stable. The difference
// is the window between an even line's release and its LTS promotion (April
// to October of the same year), during which Renovate would still hold it.
// The estate pins job images to LTS lines, and the docker tag it writes
// (24-alpine) is not a version to either scheme - what moves there is the
// digest. It lives in this package because it is npm with one method
// changed, and a layer-3 package may not import another.
type Node struct {
	Scheme
}

func NewNode() *Node { return &Node{} }

func (*Node) Name() string { return "node" }

// IsStable is true for a release of an even major from 4 on - the lines
// Node.js promotes to long-term support.
func (n *Node) IsStable(v string) bool {
	if !n.Scheme.IsStable(v) {
		return false
	}
	major, ok := n.Major(v)
	return ok && major >= 4 && major%2 == 0
}
