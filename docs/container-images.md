<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Container images

Two images per release, `linux/amd64` and `linux/arm64`. The first release
with images is **0.35.0**; the binaries on the release page go back further.

| Image | Package page | What is in it |
|---|---|---|
| `ghcr.io/ohartwig/pinup` | [pinup](https://github.com/ohartwig/pinup/pkgs/container/pinup) | pinup, `git`, `gpg` and its agent, `ssh-keygen`, a CA bundle |
| `ghcr.io/ohartwig/pinup-toolchain` | [pinup-toolchain](https://github.com/ohartwig/pinup/pkgs/container/pinup-toolchain) | the same, plus `composer`, `php`, `node`, `npm`, `yarn`, `go` |

```sh
docker run --rm -v "$PWD:/workspace" ghcr.io/ohartwig/pinup:0 \
  pinup whatif --repo . --config .pinup.yaml --report plan.json
```

`--config` names the file the run is configured by. pinup's own names are
`.pinup.yaml`, `.pinup.yml`, `.pinup.json` and `.pinup.jsonc` — YAML first
because it is the one that takes comments, and a configuration full of
rules is a thing you explain to the next reader. It reads Renovate's
`renovate.json`, `renovate.json5`, `.renovaterc` and `.renovaterc.json`
just as well, in [Renovate's configuration language](configuration.md)
either way: coming from Renovate means pointing at the file that is already
there.

## Which one

Take the slim one. pinup needs a package manager only when an update has a
**lock file to regenerate** — a `composer.lock`, a `package-lock.json`, a
`go.sum` — or when the configuration runs a `postUpgradeTask`. Everything
else (reading manifests, looking versions up, planning, writing the edit,
pushing the branch) is the binary and `git`.

The toolchain image is roughly five times the size, and every interpreter in
it is a thing that can carry a CVE into your pipeline. Where an update needs
one, pinup says so rather than guessing: the branch is held with the reason
`pluginRequired`, named in [the plan](plan-format.md), and the fix is either
this image or a plugin that owns the refresh
([Tasks and plugins](tasks-and-plugins.md)).

The slim image asserts that it stays slim: the build fails if `node` or
`php` turns up on the `PATH`. That is not decoration — it is the invariant
the image exists for, and it is checked on every build.

## Tags

| Tag | Moves | Use it for |
|---|---|---|
| `0.35.0` | never | reproducible pipelines, and anything you audit |
| `0.35` | with each patch | a minor line you want fixes for |
| `0` | with each minor | pinup is pre-1.0, so this line can carry behaviour changes |
| `latest` | with each release | trying it out |

A digest (`ghcr.io/ohartwig/pinup@sha256:…`) is the only reference that
cannot change under you; in a pipeline, pin tag **and** digest and let a
bot move both — which is, after all, what pinup is for.

## Where the binary comes from

The image does **not** compile pinup. The workflow that builds it
([`.github/workflows/images.yml`](https://github.com/ohartwig/pinup/blob/main/.github/workflows/images.yml))
downloads the binaries of that release, checks them against the release's
`SHA256SUMS`, and copies one in. The binaries are built and signed once,
upstream, and the same files are on the release page.

That split is deliberate. An earlier workflow here rebuilt the binaries when
the tag arrived, which put a second artefact under the release's name with
nobody's signature on it — packaging a signed binary is not the same as
building one, and only the first can be verified against what was published.

The recipes are in the repository:
[`packaging/Containerfile`](https://github.com/ohartwig/pinup/blob/main/packaging/Containerfile)
and
[`packaging/Containerfile.toolchain`](https://github.com/ohartwig/pinup/blob/main/packaging/Containerfile.toolchain).
The base is Chainguard's [wolfi-base](https://github.com/wolfi-dev/os),
pinned by digest; the toolchain pins each language runtime to an exact Wolfi
package version, so a bump is a reviewed diff rather than whatever the
mirror served that morning.

## Verifying what you pulled

Both images are signed **keyless**: the signature is made against the
workflow's OIDC identity, so it verifies without a key from us.

```sh
cosign verify ghcr.io/ohartwig/pinup:0.35.0 \
  --certificate-identity-regexp '^https://github.com/ohartwig/pinup/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Each image also carries an SBOM and build provenance, attached by BuildKit
as attestations in the image index:

```sh
docker buildx imagetools inspect ghcr.io/ohartwig/pinup:0.35.0 \
  --format '{{ json .SBOM }}'
```

Before the signature is made, the pushed image is scanned and a **fixable**
CRITICAL or HIGH fails the build — an image that does not pass is never
signed, so a missing signature is a statement and not an oversight.

## Running it

The image runs as uid 1000 in `/workspace`. **There is no entrypoint**, so
`pinup` is part of the command — `docker run … pinup whatif …`, not
`docker run … whatif …`, which fails with `executable file not found`. That
is deliberate: a CI runner starts the job script with `sh` in the container,
and an entrypoint of `pinup` would answer that with "unknown command sh".

```sh
docker run --rm \
  -v "$PWD:/workspace" \
  -e PINUP_GITLAB_URL -e PINUP_GITLAB_TOKEN \
  ghcr.io/ohartwig/pinup:0 \
  pinup run --project group/project --config 'local>group/runner' --report plan.json
```

- **Credentials** come from the environment, one per platform
  ([Platforms](platforms.md)). A run that only plans (`whatif`) needs none.
- **git ownership**: `safe.directory` is set system-wide in the image, so a
  mounted checkout owned by another uid does not stop git.
- **Signing**: `PINUP_SIGNING_FORMAT=openpgp` needs the key handed in — the
  image has `gpg` and its agent, and no key. `none` is the default.
- **The cache** (`PINUP_CACHE`) belongs on a volume that survives the run:
  it holds release lists, first-seen records, advisories and release notes,
  and an empty cache makes `minimumReleaseAge` unreliable for registries
  that publish no timestamps. [Getting started](getting-started.md#a-ci-job)
  has the shape of that in a scheduled job.

## In a pipeline

[`examples/gitlab-ci.yml`](examples/gitlab-ci.yml) and
[`examples/github-actions.yml`](examples/github-actions.yml) both run on
these images. The short version, for GitLab:

```yaml
pinup:scan:
  image: ghcr.io/ohartwig/pinup-toolchain:0
  script:
    - pinup run --autodiscover '["group/**"]' --config 'local>group/runner' --report 'reports/%s.json'
```

If your runners cannot reach `ghcr.io`, mirror the image into your own
registry and pin the digest you mirrored: an image you copied is an image
you can still verify, as long as the digest travels with it.
