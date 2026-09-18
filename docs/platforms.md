<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Platforms

The datasources and managers are platform-neutral. The platform is where
the merge requests, the dashboard issue, the `local>` presets and the
project listing live: an interface (`publish.Platform`) with two
implementations.

## GitLab

Proven in production across an estate of two hundred repositories.

| Variable | |
|---|---|
| `PINUP_GITLAB_URL` / `CI_SERVER_URL` | the instance |
| `PINUP_GITLAB_TOKEN` / `GITLAB_TOKEN` | a personal or project access token with `api` and `write_repository`; `CI_JOB_TOKEN` is used when none is set and can only read |
| `PINUP_REGISTRY_HOST` / `CI_REGISTRY` | the instance's container registry, where the token is exchanged for a pull token |

What the platform does: merge requests found by source branch (open
only; one a person closed without merging holds the same edits as
`closedByHand` and is not reopened), created and updated
with title, description, labels and merge-when-pipeline-succeeds;
automerge refused by permission is reported beside the request, not as
a failure; a request whose branch the plan no longer names is closed as
autoclosed; the commit signature verdict read from the API, so a run can
tell whether its commits show Verified; the dashboard issue found among
the bot's own issues by exact title (an issue anyone else opened with the
title is not the dashboard); `local>` presets read through the raw-file
endpoint; autodiscovery over every project the token is a member of that
is not archived.

`pinup token rotate` renews the bot's own token before it expires and
writes the new one into the CI variables it names, at group or project
scope. A job token cannot rotate itself.

## GitHub

Proven against a fake that speaks the API and in a read-only run against
pinup's own mirror; waiting for its first production repository.

| Variable | |
|---|---|
| `PINUP_PLATFORM=github` | or implied by a GitHub token with no GitLab instance in the environment |
| `PINUP_GITHUB_URL` / `GITHUB_SERVER_URL` | the host; `github.com` when unset; an enterprise host is reached under `/api/v3` |
| `PINUP_GITHUB_TOKEN` / `GITHUB_TOKEN` | classic `repo`, or fine-grained with contents, pull requests and issues read/write; on github.com it serves the `github-*` datasources as well |

Pull requests are found by head as `owner:branch` (a fork's branch of
the same name is another request), created and brought in line (title,
body, labels); auto-merge is armed and disarmed through GraphQL, the one
API GitHub offers for it, and "not yet" (nothing to wait for) is told
apart from "not allowed" (the repository setting), which is reported
beside the request. git authenticates through the askpass as
`x-access-token`. The dashboard is the bot's own issue by exact title;
the issues endpoint's pull requests are left out. Autodiscovery lists
the repositories the token can push to that are not archived.

What GitHub has no equivalent for is said, not faked: a request cannot
ask for its branch to be deleted on merge (that is the repository's
`delete_branch_on_merge` setting), and auto-merge needs the repository to
allow it.

## Adding one

An implementation is one package under `platform/` with the methods of
`publish.Platform`, an `iface.go` asserting it, a test server that speaks
the protocol (pagination included) behind the refusing transport, one
case in `wire.Platform` and one in the environment reader. Gitea and
Forgejo are close to GitLab's shape.
