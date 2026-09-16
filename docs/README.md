<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: MIT
-->

# pinup documentation

For people who run pinup:

| Page | What it answers |
|---|---|
| [Getting started](getting-started.md) | the binary, a token, the first plan, the first run, a CI job |
| [Commands](commands.md) | every subcommand and its flags |
| [Configuration](configuration.md) | where configuration comes from, how it merges, which keys pinup reads |
| [Managers, datasources, versionings](managers-and-datasources.md) | what pinup extracts, where it looks versions up, how it orders them |
| [The plan](plan-format.md) | the JSON every run writes before it writes anything else |
| [Effective classification](effective-classification.md) | the analyzer's label beside the version's, and the rules that read it |
| [Tasks and plugins](tasks-and-plugins.md) | lock refreshes and post-upgrade commands: what runs, in what scope, with what |
| [Platforms](platforms.md) | GitLab and GitHub: credentials, what each can and cannot do |
| [Security](security.md) | the invariants, the credential paths, what a repository can and cannot make the bot do |
| [Examples](examples/) | a complete runner configuration, a scheduled GitLab job, a GitHub Actions workflow |

For people who change pinup: the architecture notes (`plan.md`) and the
specification with its measured corrections (`tech-spec.md`) live with the
development repository on the author's GitLab, not in the public mirror.
