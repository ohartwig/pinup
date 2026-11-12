// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package preset

// How a key combines when a later layer - a preset listed later, or the
// configuration's own keys - sets it again. Measured, not designed: the
// tables are what the pinned container does for every option of its own
// (tools/capture/merge.sh, testdata/renovate/<version>/config/merge.ndjson),
// and TestMergeTablesMatchTheCapture fails when they and the capture part.
//
// Three outcomes:
//
//   - concatKeys: the later array is appended to the earlier one;
//   - mergeKeys: the objects combine key by key, and each key inside
//     combines by these same tables - so vulnerabilityAlerts.addLabels
//     appends, while vulnerabilityAlerts.labels and any nested object that
//     is not itself such a key are replaced;
//   - every other key, pinup's own included, is replaced.
//
// A later null replaces whatever was there (and then clears it, see
// config.Merge); a later empty array or empty object adds nothing, so it
// leaves the earlier value standing - an empty list is not a way to clear
// one in Renovate, and is not in pinup.
//
// pinup's own keys are not Renovate options and replace, as an unknown key
// does there: artifactManifests and prConcurrentLimitIgnoreLabels are read
// whole from the layer that set them last.

// concatKeys are the keys whose arrays concatenate.
var concatKeys = map[string]bool{
	"addLabels": true, "additionalReviewers": true, "allowedEnv": true, "commitTrailers": true,
	"customManagers": true, "description": true, "hostRules": true, "ignoreDeps": true,
	"logLevelRemap": true, "managerFilePatterns": true, "matchBaseBranches": true,
	"matchCategories": true, "matchConfidence": true, "matchDatasources": true,
	"matchDepNames": true, "matchDepTypes": true, "matchJsonata": true, "matchManagers": true,
	"matchPackageNames": true, "matchRegistryUrls": true, "matchRepositories": true,
	"matchSourceUrls": true, "matchUpdateTypes": true, "packageRules": true,
	"postUpdateOptions": true, "prBodyNotes": true, "suppressNotifications": true,
}

// mergeKeys are the keys whose objects merge key by key. Most are manager
// names: a manager's own object (gomod, dockerfile, ...) merges like an
// update type's.
var mergeKeys = map[string]bool{
	"ansible": true, "ansible-galaxy": true, "ant": true, "argocd": true, "asdf": true,
	"azure-pipelines": true, "batect": true, "batect-wrapper": true, "bazel": true,
	"bazel-module": true, "bazelisk": true, "bicep": true, "bitbucket-pipelines": true,
	"bitrise": true, "buildkite": true, "buildpacks": true, "bun": true, "bun-version": true,
	"bundler": true, "cake": true, "cargo": true, "cdnurl": true, "circleci": true,
	"cloudbuild": true, "cocoapods": true, "composer": true, "conan": true, "constraints": true,
	"constraintsVersioning": true, "copier": true, "cpanfile": true, "crossplane": true,
	"crow": true, "customDatasources": true, "deno": true, "deps-edn": true, "devbox": true,
	"devcontainer": true, "digest": true, "docker-compose": true, "dockerfile": true,
	"droneci": true, "fleet": true, "flux": true, "force": true, "fvm": true,
	"git-submodules": true, "github-actions": true, "gitlabci": true, "gitlabci-include": true,
	"glasskube": true, "gleam": true, "gomod": true, "gradle": true, "gradle-wrapper": true,
	"group": true, "haskell-cabal": true, "helm-requirements": true, "helm-values": true,
	"helmfile": true, "helmsman": true, "helmv3": true, "hermit": true,
	"homeassistant-manifest": true, "homebrew": true, "html": true, "jenkins": true,
	"jsonata": true, "jsonnet-bundler": true, "kotlin-script": true, "kubernetes": true,
	"kustomize": true, "leiningen": true, "lockFileMaintenance": true, "major": true,
	"maven": true, "maven-wrapper": true, "meteor": true, "minor": true, "mint": true,
	"mise": true, "mix": true, "nix": true, "nodenv": true, "npm": true, "nuget": true,
	"nvm": true, "ocb": true, "onboardingConfig": true, "osgi": true, "patch": true,
	"pep621": true, "pep723": true, "pin": true, "pinDigest": true, "pip-compile": true,
	"pip_requirements": true, "pip_setup": true, "pipenv": true, "pixi": true, "poetry": true,
	"prBodyDefinitions": true, "prBodyHeadingDefinitions": true, "pre-commit": true,
	"productLinks": true, "proto": true, "pub": true, "puppet": true, "pyenv": true,
	"quadlet": true, "regex": true, "registryAliases": true, "renovate-config": true,
	"replacement": true, "rollback": true, "ruby-version": true, "runtime-version": true,
	"rust-toolchain": true, "sbt": true, "scalafmt": true, "secrets": true, "setup-cfg": true,
	"smithy": true, "statusCheckNames": true, "statusCheckWhen": true, "sveltos": true,
	"swift": true, "tekton": true, "terraform": true, "terraform-version": true,
	"terragrunt": true, "terragrunt-version": true, "tflint-plugin": true, "travis": true,
	"typst": true, "unity3d": true, "variables": true, "velaci": true, "vendir": true,
	"vulnerabilityAlerts": true, "woodpecker": true, "xcodegen": true,
}
