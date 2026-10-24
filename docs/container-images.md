<!--
SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
SPDX-License-Identifier: Apache-2.0
-->

# Container images

Two images per release, `linux/amd64` and `linux/arm64`. The first release
with images is **0.35.0**; the binaries on the release page go back further.
A release that a newer one replaces within minutes can be skipped: the
images follow the release the author's own pipeline runs.

| Image | Package page | What is in it |
|---|---|---|
| `ghcr.io/ohartwig/pinup` | [pinup](https://github.com/ohartwig/pinup/pkgs/container/pinup) | pinup, `git`, `gpg` and its agent, `ssh-keygen`, a CA bundle |
| `ghcr.io/ohartwig/pinup-toolchain` | [pinup-toolchain](https://github.com/ohartwig/pinup/pkgs/container/pinup-toolchain) | the same, plus `composer`, `php`, `node`, `npm`, `yarn`, `pnpm`, `go` |

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
| `0.35.0` | only when that release is rebuilt on a patched base | a fixed pinup version |
| `0.35` | with each patch | a minor line you want fixes for |
| `0` | with each minor | pinup is pre-1.0, so this line can carry behaviour changes |
| `latest` | with each release | trying it out |

The tags name the pinup version inside the image. The image itself is
rebuilt whenever its base or its packages get a fix, and a rebuild of the
same pinup release moves that release's tag to the new image; the rolling
tags only ever move forward. A digest (`ghcr.io/ohartwig/pinup@sha256:…`)
is the only reference that cannot change under you; in a pipeline, pin tag
**and** digest and let a bot move both — which is, after all, what pinup is
for.

## Where the image comes from

Nothing is built on GitHub. The images are built once, in the author's
pipeline, as the images that pipeline runs pinup in itself: the binary of
the release is downloaded, checked against the release's `SHA256SUMS` and
copied onto a Wolfi base pinned by digest; the toolchain image adds each
language runtime pinned to an exact Wolfi package version, so a bump is a
reviewed diff rather than whatever the mirror served that morning. The
image is scanned, and a CRITICAL or HIGH finding that is neither documented
as not affecting it (VEX) nor explicitly accepted fails the build; only then
is it signed and released. What is published here is that image, copied by
digest — the same bytes, the same signature.

That split is deliberate. These images used to be built a second time by a
workflow in this repository, from a copy of the recipe on a different base:
a second artefact under the release's name, with its own scan result and
its own signature, which drifted from the one actually in use. The same
reasoning removed an earlier workflow that rebuilt the binaries — packaging
a signed artefact once is the only way the published one can be verified
against what was built.

The labels say what is inside: `org.opencontainers.image.version` is the
pinup release, `org.opencontainers.image.source` this repository.

## Verifying what you pulled

Both images are signed with the same key as the release's `SHA256SUMS`. Its
public half is [`cosign.pub`](../cosign.pub) in this repository; the
private half is held in AWS KMS and never leaves it.

```sh
cosign verify --key cosign.pub --insecure-ignore-tlog=true \
  ghcr.io/ohartwig/pinup:0
```

`--insecure-ignore-tlog=true` because the signatures are not recorded in
the public Sigstore transparency log; the key is the trust anchor, and a
signature that verifies against it was made by the pipeline that holds it.
Verify a digest rather than a tag where you can — a signature covers the
digest, and that is what you pin.

Each image also carries a CycloneDX SBOM as a signed attestation:

```sh
cosign verify-attestation --key cosign.pub --insecure-ignore-tlog=true \
  --type cyclonedx ghcr.io/ohartwig/pinup:0
```

Images up to 0.65.7 were built by a GitHub workflow and signed keyless;
those signatures verify as before, against
`--certificate-identity-regexp '^https://github.com/ohartwig/pinup/'` and
`--certificate-oidc-issuer https://token.actions.githubusercontent.com`.
Its last runs, 0.65.8 to 0.66.0, pushed images that then failed its scan
and were never signed; a tag of those versions that verifies against
`cosign.pub` has since been replaced from the pipeline.

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
