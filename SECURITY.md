<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: MIT
-->

# Security policy

## Reporting a vulnerability

Please do not open a public issue or pull request for a security problem.
Send a report to <security@ole-hartwig.eu>; a PGP key for attachments is
published at <https://ole-hartwig.eu/.well-known/openpgpkey> (RFC 9580).

You will get an acknowledgement within 72 hours on business days and a
triage result within 7 days. Findings stay embargoed until a fix is released,
90 days after first response at the latest, and the reporter is credited
unless they prefer not to be.

## What counts

In scope: the `pinup` binary and the container image built from this
repository — in particular anything that lets a repository's configuration
read or exfiltrate the platform token, the signing key or a registry
credential; run a command the runner's allowlist did not admit; write
outside the file scope a task was given; or make the bot act in a merge
request or issue beyond what it planned (quick actions, mentions,
approvals).

Out of scope: the forges and registries pinup talks to (report those to
GitLab, GitHub or the registry's operator), and denial of service against
the CI runner.

## Supported versions

The latest minor release. Fixes are released as patch versions and noted in
the changelog with a `security` mention.
