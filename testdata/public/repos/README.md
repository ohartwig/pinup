# Synthetic repositories

Written for the twin configuration under `../config`, each in the shape of
a kind of repository the configuration serves, and extracted by the pinned
Renovate container into `../renovate-43.288.0/extract/<name>.json`
(`PINUP_FIXTURES=testdata/public tools/capture/extract-corpus.sh
testdata/public/repos/<name>`).

| Tree | Exercises |
|---|---|
| `ci-image` | `dockerfile` (digest-pinned FROMs, a `--with` Go tool, the vendor-CLI ARG), `gitlabci` (components at a full version and a rolling `@N`, a `default:` image, `*_image` variables, a tag with a variable in it), the annotated-line custom managers on the pipeline, a template, a shell script and a compose file |
| `website` | `composer` (TYPO3, first-party packages through the nested-template manager, a `suggest` block the manager over-matches, the PHP platform pin), `gitlabci`, a scheduler `Containerfile`, a compose file |
| `frontend` | `npm` (the four sections, `engines.node`, `packageManager`, `pnpm.overrides`), `gitlabci` |
| `infra` | `terraform` (required providers with and without a version, local and registry modules, a lock from the default registry), an annotated variable |
| `gitops` | the annotated `tag:`/image lines of `tenants/`, `charts/` and `bootstrap/` in every form the configuration reads, `Chart.yaml` app versions, the argocd install manifests, `gitlabci` |

Nothing here is copied from anywhere; the trees are authored to cover the
configuration's managers, not to resemble any real repository.
