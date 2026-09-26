// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package githubds

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/lookup"
)

// maxTagHops bounds the walk from a tag object to its commit. An annotated
// tag normally points straight at a commit; git allows a tag of a tag, and
// a chain longer than this is treated as broken rather than followed on.
const maxTagHops = 4

// Digest answers the commit SHA a tag points to - what a GitHub Actions
// reference pinned by SHA (`uses: owner/repo@<sha> # v1.2.3`) must move to
// when its version does.
//
// THE ENDPOINT, from the documented REST contract (docs.github.com/rest,
// "Git database" API):
//
//	GET /repos/{owner}/{repo}/git/ref/tags/{tag}
//	  -> {"ref": "refs/tags/v1.2.3", "object": {"sha": "…", "type": "commit" | "tag"}}
//	GET /repos/{owner}/{repo}/git/tags/{sha}      (only for type "tag")
//	  -> {"object": {"sha": "…", "type": "commit" | "tag"}}
//
// Chosen over the two alternatives the API offers:
//
//   - "GET /repos/{owner}/{repo}/commits/{ref}" peels in one request, but
//     resolves {ref} against branches as well as tags - a branch named like
//     the tag would answer for it - and returns the whole commit with its
//     file list, which on a large release is megabytes for forty hex digits.
//   - "GET /repos/{owner}/{repo}/tags" carries each tag's commit, but only
//     as a paginated listing; finding one tag there is up to ten requests.
//
// git/ref (singular) matches the ref exactly - the plural git/refs does a
// prefix match and would answer v1.2 with v1.2.3 - and lives in the tags
// namespace only, so what comes back is that tag or a 404.
//
// An ANNOTATED tag is a git object of its own: git/ref answers the tag
// object's SHA with type "tag", and that SHA is not a commit. Pinning it
// would write a SHA no checkout of the action resolves the way the file
// claims, so the tag object is dereferenced through git/tags until a
// commit is reached. A lightweight tag answers the commit directly.
func (d *Datasource) Digest(ctx context.Context, ref lookup.Ref, version string) (string, error) {
	owner, repo, err := splitOwnerRepo(ref.PackageName)
	if err != nil {
		return "", fmt.Errorf("%s: %w", d.kind, err)
	}
	if version == "" {
		return "", fmt.Errorf("%s: %s/%s: no tag to resolve", d.kind, owner, repo)
	}
	base := d.baseFor(ref)

	var obj gitObject
	if err := d.getJSON(ctx, fmt.Sprintf("%s/repos/%s/%s/git/ref/tags/%s", base, owner, repo, escapeRef(version)), &gitRef{Object: &obj}); err != nil {
		if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("%s: %s/%s has no tag %q (or the token cannot read the repository)", d.kind, owner, repo, version)
		}
		return "", wrapError(d.kind, owner, repo, err)
	}
	for range maxTagHops {
		switch obj.Type {
		case "commit":
			if !isCommitSHA(obj.SHA) {
				return "", fmt.Errorf("%s: %s/%s: tag %q names %q, which is not a commit SHA", d.kind, owner, repo, version, obj.SHA)
			}
			return obj.SHA, nil
		case "tag":
			var next gitTag
			if err := d.getJSON(ctx, fmt.Sprintf("%s/repos/%s/%s/git/tags/%s", base, owner, repo, url.PathEscape(obj.SHA)), &next); err != nil {
				return "", wrapError(d.kind, owner, repo, err)
			}
			obj = next.Object
		default:
			// A tag may point at a tree or a blob. Neither is something a
			// workflow can run.
			return "", fmt.Errorf("%s: %s/%s: tag %q points to a %q, not a commit", d.kind, owner, repo, version, obj.Type)
		}
	}
	return "", fmt.Errorf("%s: %s/%s: tag %q is a chain of more than %d tag objects", d.kind, owner, repo, version, maxTagHops)
}

type gitObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type gitRef struct {
	Ref    string     `json:"ref"`
	Object *gitObject `json:"object"`
}

type gitTag struct {
	Object gitObject `json:"object"`
}

func (d *Datasource) getJSON(ctx context.Context, u string, out any) error {
	resp, err := d.client.Get(ctx, u, httpx.ReqOptions{Accept: "application/vnd.github+json"})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	return nil
}

// escapeRef escapes a tag name for the URL path segment by segment: a tag
// may contain slashes (release/1.0), which the ref endpoint takes as they
// are, and anything else a path cannot carry is escaped.
func escapeRef(tag string) string {
	parts := strings.Split(tag, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// isCommitSHA reports a full SHA-1 commit id, forty lowercase hex digits,
// the only form a workflow may pin an action to.
func isCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
