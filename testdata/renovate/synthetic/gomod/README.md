# Synthetic gomod tree

No estate repository runs Renovate's `gomod` manager (`enabledManagers` in the
runner config is a closed set without it), yet pinup updates itself with it.
Captured by executing the pinned container over this tree with
`<root>/config/gomod.json` (`enabledManagers: ["gomod"]` and nothing
else), so the vectors record the manager's own defaults rather than the
runner's rules. Shapes: block and single-line `require`, `// indirect`,
`replace`, `exclude`, `toolchain`, `tool`, and a nested module.
