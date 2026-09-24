// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

// Package publish declares what a platform must do for a plan's branches:
// find the merge request that already exists for a branch, open one, bring
// one up to date, and set or clear its automerge.
//
// Layer 2. It knows the shapes and nothing about GitLab; platform/* fills
// them in and wire hands them over. A platform that cannot do something
// returns an error the runner records as a warning against that branch -
// one project's 403 is a warning naming that project, never a failed run
// for the rest.
package publish

import (
	"context"
	"time"
)

// Project identifies a repository on the platform.
type Project struct {
	// Path is the full path, "devops/images/ci-tools".
	Path string
	// DefaultBranch is where merge requests target.
	DefaultBranch string
}

// MergeRequest is what the platform knows about one open or closed
// request for a branch.
type MergeRequest struct {
	IID          int
	State        string // "opened", "merged", "closed"
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
	Labels       []string
	// SHA is the head of the source branch as the platform sees it.
	SHA       string
	WebURL    string
	Automerge bool
	// AutomergeRefused says why the platform would not arm automerge for
	// a request that asked for it - the bot may not merge in this
	// project - so the run can report it beside the request it did open.
	AutomergeRefused string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Request is what a branch wants its merge request to look like.
type Request struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
	Labels       []string
	// Automerge asks the platform to merge when the pipeline succeeds -
	// GitLab's merge-when-pipeline-succeeds. Cleared when false.
	Automerge bool
	// AutomergeDirect allows the platform to merge a request outright when
	// it would not arm automerge and says the request is mergeable now -
	// every check already passed, so there is nothing left to wait for.
	// Without it such a request stays open until a later run.
	//
	// It only ever applies where Automerge is set: it changes how an
	// automerge is carried out, never whether one happens.
	AutomergeDirect bool
	// RemoveSourceBranch is set on the request so a merge cleans up.
	RemoveSourceBranch bool
	// HeadSHA is the head this run pushed, when it pushed. Automerge is
	// armed for exactly that commit. The platform's own record of the head
	// can lag a push by seconds, and arming the old head gets the
	// automerge aborted the moment the platform catches up - "aborted the
	// automatic merge because the source branch was updated", every hour
	// on devops/koh-gitops!2878 (2026-09-24), whose base moved each run.
	HeadSHA string
}

// Platform is the merge-request side of a run.
type Platform interface {
	Name() string

	// Project resolves a path to the project and its default branch.
	Project(ctx context.Context, path string) (Project, error)

	// FindMergeRequest returns the open merge request for a source branch,
	// or ok=false when there is none. Closed and merged requests are not
	// returned here: a branch whose request was closed by a person is a
	// branch nobody wants reopened, which the caller decides with History.
	FindMergeRequest(ctx context.Context, p Project, sourceBranch string) (mr MergeRequest, ok bool, err error)

	// History lists every merge request there ever was for a source
	// branch, newest first, so a closed one can be respected.
	History(ctx context.Context, p Project, sourceBranch string) ([]MergeRequest, error)

	// CreateMergeRequest opens one.
	CreateMergeRequest(ctx context.Context, p Project, r Request) (MergeRequest, error)

	// UpdateMergeRequest brings an existing one in line with r: title,
	// description, labels, automerge. It changes nothing that already
	// matches, and reports what it changed.
	UpdateMergeRequest(ctx context.Context, p Project, iid int, r Request) (MergeRequest, []string, error)

	// CloseMergeRequest closes one, retitled - what a run does with a
	// request whose update no longer exists: the value it carried is on
	// the base by another road, or the rule that asked for it is gone.
	CloseMergeRequest(ctx context.Context, p Project, iid int, title string) error

	// CommitVerification reports how the platform judged a commit's
	// signature: "verified", "unverified", or the platform's own word.
	CommitVerification(ctx context.Context, p Project, sha string) (string, error)

	// ReadFile returns a file from a project at ref (empty means the
	// default branch) - how local> presets are fetched.
	ReadFile(ctx context.Context, project, path, ref string) ([]byte, error)

	// ListProjects returns the paths of every project the token can see
	// that is not archived, sorted. Autodiscovery filters this list.
	ListProjects(ctx context.Context) ([]string, error)

	// OpenMergeRequests lists every open merge request of a project whose
	// source branch starts with prefix - the other tool's side of the
	// shadow comparison.
	OpenMergeRequests(ctx context.Context, p Project, prefix string) ([]MergeRequest, error)
	// MergedMergeRequests lists the merge requests of a project whose source
	// branch starts with prefix and that were merged at or after since: a
	// branch the other tool opened and had merged is a match the open list
	// no longer shows.
	MergedMergeRequests(ctx context.Context, p Project, prefix string, since time.Time) ([]MergeRequest, error)

	// UpsertIssue finds the open issue of a project with exactly this
	// title and brings its description and labels in line, or opens it.
	// It reports the issue and whether anything changed. The rolling-major
	// notice is one such issue: a report that stays, updated per run.
	UpsertIssue(ctx context.Context, p Project, title, description string, labels []string) (Issue, bool, error)
	// ReadIssue finds the open issue with exactly this title and returns
	// it with its description - what the dashboard's ticked boxes are read
	// from before a run plans.
	ReadIssue(ctx context.Context, p Project, title string) (Issue, string, bool, error)
}

// Issue is an issue on the platform, as much of it as pinup reads back.
type Issue struct {
	IID   int
	Title string
	URL   string
	State string
}
