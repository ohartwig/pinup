# The public fixture root

A fixture root of the same shape as the estate's own (`testdata/estate`,
filtered out of the public mirror), for a fictional estate on
`git.acme.test`:

- `config/` - the synthetic twin of a runner configuration (see its README);
- `repos/` - five small repositories written for it: a CI tooling image, a
  TYPO3 website, an npm frontend, a terraform tree, a gitops tree - plus the
  shared synthetic `gomod` and `tfversion` trees under `testdata/renovate`;
- `renovate-43.288.0/` - what the pinned Renovate container answered for
  them: the configuration resolved, the corpus extracted, 1728 rule vectors;
- `golden/` - six golden repositories with the live answers recorded once
  and the plan pinup made from them, replayed byte for byte;
- `expect.json` - the denominators the tests measure against.

`PINUP_FIXTURES=testdata/public go test ./...` runs the suite here; CI's
`test:go:public` does so on every push. Anything with the estate's names in
it belongs in the other root.
