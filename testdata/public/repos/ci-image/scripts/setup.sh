#!/bin/sh
set -eu
# renovate: datasource=github-releases depName=cli/cli
GH_VERSION=2.79.0
# renovate: datasource=github-releases depName=gitleaks/gitleaks packageName=gitleaks/gitleaks versioning=semver
GITLEAKS_VERSION=8.28.0
# renovate: datasource=npm depName=semantic-release
SEMANTIC_RELEASE_VERSION=24.2.7
install-tool gh "$GH_VERSION"
