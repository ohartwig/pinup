# Fix fixtures, v1

`commented.jsonc` is a configuration in the shape the estate's `.pinup.jsonc`
files take - a head comment, a trailing comment after a member, a block
comment, one-line rule objects, trailing commas. `commented.expected.jsonc` is
what `advise.Apply` makes of it with the six fixes `TestFixesKeepComments`
lists. The expected file is a golden: it is read, never rewritten. A change in
the rewriter's output is a new directory `v2/`, reviewed as a diff.
