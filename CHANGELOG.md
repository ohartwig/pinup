## [0.7.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.6.3...v0.7.0) (2026-09-12)

### :sparkles: Features

* **shadow:** a stale Renovate merge request can be triaged ([8724d24](https://git.ole-hartwig.eu/pinup/pinup/commit/8724d24ffec266daf14233ed0198cf77f73b33a9))

### :memo: Documentation

* **tasks:** D.13 rehearsed green, D.15 delivered ([bc0fb6e](https://git.ole-hartwig.eu/pinup/pinup/commit/bc0fb6e5ae3858c69491d92c66f6b3428ec92c34))
* **tasks:** raise-epoch was already allowed upstream ([02876f6](https://git.ole-hartwig.eu/pinup/pinup/commit/02876f61e9713bba631b955cd00b25b9e9ecd14b))
* **tasks:** the first green shadow verdict ([7f1e92f](https://git.ole-hartwig.eu/pinup/pinup/commit/7f1e92fa7517e40699e54a96173a69bc6db796b5))
* **tasks:** the golang image carries git; nothing skips in CI ([e483c2e](https://git.ole-hartwig.eu/pinup/pinup/commit/e483c2e8006ed7449b91c54e92573563978616c9))

### :repeat: Continuous Integrations

* update:pinup runs on the toolchain image with the built binary ([6548842](https://git.ole-hartwig.eu/pinup/pinup/commit/654884280e4f3eb89a8e5a779804299fd7b2c1fb))

## [0.6.3](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.6.2...v0.6.3) (2026-09-12)

### :bug: Fixes

* **planner:** update-lockfile without a lock moves nothing; config-less repositories ignore node_modules ([71825ba](https://git.ole-hartwig.eu/pinup/pinup/commit/71825ba5008586425fbd6f6e64b2dbebcdcfd169))

## [0.6.2](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.6.1...v0.6.2) (2026-09-12)

### :bug: Fixes

* **advisories:** an OR of ranges is not asked; composer grows one by a major line ([c345098](https://git.ole-hartwig.eu/pinup/pinup/commit/c3450989c37c986c6d1dc17fe049d4c21f1381c6))

### :memo: Documentation

* **tasks:** P1e.9 measurement and the prepared toolchain image ([aad16d7](https://git.ole-hartwig.eu/pinup/pinup/commit/aad16d7a10ca281d589d3ee0b930e2dd77a6bda2))
* **tasks:** the rotation rehearsal found the role gap ([00e30c5](https://git.ole-hartwig.eu/pinup/pinup/commit/00e30c55b77edb3c6b3e670cae72022a05645265))

## [0.6.1](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.6.0...v0.6.1) (2026-09-12)

### :fast_forward: Performance

* **run:** blobless clones and four repositories at a time ([b97fe97](https://git.ole-hartwig.eu/pinup/pinup/commit/b97fe9772462898f41c4571af1216fd2124bcd75))

## [0.6.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.5.0...v0.6.0) (2026-09-12)

### :sparkles: Features

* **planner:** regex matches stay unpinned, bump raises ranges, advisories for ranges, node versioning ([cf76ffc](https://git.ole-hartwig.eu/pinup/pinup/commit/cf76ffc2049156ebdb5b54b5750e1b7d9c751548))

## [0.5.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.4.0...v0.5.0) (2026-09-12)

### :sparkles: Features

* **token:** pinup token rotate renews the bot's token before it expires ([3ec45cf](https://git.ole-hartwig.eu/pinup/pinup/commit/3ec45cf8670c01cb0dbc86307c9a48dca23fd4c3))

## [0.4.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.3.0...v0.4.0) (2026-09-12)

### :sparkles: Features

* git-refs, Go pseudo-versions and go.sum, disabled dependencies the advisory check still sees ([f86d74a](https://git.ole-hartwig.eu/pinup/pinup/commit/f86d74a6631112788e623d7d30fa051cc50733ff))

### :repeat: Continuous Integrations

* keep trivy out of testdata ([8c49dcb](https://git.ole-hartwig.eu/pinup/pinup/commit/8c49dcb0b6862627bc4e920ed5c121baec401644))

## [0.3.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.2.0...v0.3.0) (2026-09-12)

### :sparkles: Features

* **planner:** range strategies, pinDigests, security fixes from the lock; the comparator learns time ([03a7197](https://git.ole-hartwig.eu/pinup/pinup/commit/03a71978484e8fc714c9df01c8f430667d4a7648))

### :white_check_mark: Tests

* **mutation:** re-anchor the unchanged-value mutator ([b5ecfe0](https://git.ole-hartwig.eu/pinup/pinup/commit/b5ecfe02a3c821675d573284532e22c6b50e4b33))

## [0.2.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.1.1...v0.2.0) (2026-09-12)

### :sparkles: Features

* **run:** phase timings on the summary line ([a165aae](https://git.ole-hartwig.eu/pinup/pinup/commit/a165aae7f9126ae5c807a11009fa676c41a02f34))

### :memo: Documentation

* **tasks:** delivery live - credentials, releases, image, schedules, first-run findings ([68d20b5](https://git.ole-hartwig.eu/pinup/pinup/commit/68d20b5233facc69758fb1a31ef25881b78d816e))

## [0.1.1](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.1.0...v0.1.1) (2026-09-12)

### :bug: Fixes

* **cli:** embed the zone database - a job image without tzdata broke every schedule ([514fbb1](https://git.ole-hartwig.eu/pinup/pinup/commit/514fbb1d8482b8cb985fd35e99b739e62fc30b6b))

### :memo: Documentation

* **tasks:** D.15 - the toolchain image for the composer/npm partitions ([f22ebcf](https://git.ole-hartwig.eu/pinup/pinup/commit/f22ebcf53561ee4d0bf388622fa65c5d9171bd9e))

## [0.1.0](https://git.ole-hartwig.eu/pinup/pinup/compare/v0.0.0...v0.1.0) (2026-09-12)

### :sparkles: Features

* **apkindex:** parse APKINDEX and answer what every architecture has ([a527ccf](https://git.ole-hartwig.eu/pinup/pinup/commit/a527ccfbb00ef3ca30bd519c3c2b8cf6b7df5b48))
* **apply:** byte-range applier, and branches carry their edits ([7bc5ec5](https://git.ole-hartwig.eu/pinup/pinup/commit/7bc5ec58de1302366ae762cbe487fef9d05101c8))
* **cache:** the bbolt store, with firstseen treated as correctness ([c075162](https://git.ole-hartwig.eu/pinup/pinup/commit/c0751625e054286603ab6b4f2b4cc25aeb94e38d))
* **capture:** build an extraction corpus from real repositories ([c8f247c](https://git.ole-hartwig.eu/pinup/pinup/commit/c8f247c6bbf0fd215eaacfd714faca42703d5e44)), closes [#10](https://git.ole-hartwig.eu/pinup/pinup/issues/10) [#10](https://git.ole-hartwig.eu/pinup/pinup/issues/10)
* **capture:** record packageRules resolution for 3150 dependency vectors ([fe22a69](https://git.ole-hartwig.eu/pinup/pinup/commit/fe22a693b5306975bd428df078e5fb76a5f80f8b))
* **capture:** record Renovate's resolved config by running it ([2d42ea4](https://git.ole-hartwig.eu/pinup/pinup/commit/2d42ea49d951c0daf6715a26d82be9ff8af9df98))
* **capture:** record versioning behaviour for ten schemes ([46d5727](https://git.ole-hartwig.eu/pinup/pinup/commit/46d5727a92fc6a833bf62202ac7c938d53e5f00b))
* **capture:** widen the corpus to 350 vectors and record the yasrt boundary ([a416ec9](https://git.ole-hartwig.eu/pinup/pinup/commit/a416ec9119d4c134c008dc4b5f89264f73779afb))
* **ci:** update:pinup - dogfooding from HEAD on a schedule ([048b0ce](https://git.ole-hartwig.eu/pinup/pinup/commit/048b0ce53a070b69a981265ab1d57a2cc84bd30b))
* **cli:** the plan's Markdown rendering beside every report ([5e16671](https://git.ole-hartwig.eu/pinup/pinup/commit/5e1667185474523b624be753f8a74c5f8552e524))
* **cmd:** whatif runs end to end against a real repository ([2c9ead3](https://git.ole-hartwig.eu/pinup/pinup/commit/2c9ead30717beba62a6ecaca028bdaf821875e84)), closes [custom.regex#13](https://git.ole-hartwig.eu/pinup/custom.regex/issues/13)
* **config:** add layer loading and the merge engine with provenance ([b105701](https://git.ole-hartwig.eu/pinup/pinup/commit/b1057017ec53767bc34f6539f8023bc6dd2ddf62))
* **config:** JSON5 configuration files; measured // preset paths; whatif reads local> presets ([cb817b6](https://git.ole-hartwig.eu/pinup/pinup/commit/cb817b60c0b75a1e72ab4548453bc73304e7aa3d))
* **config:** resolve a repository's own renovate.json through the runner alias ([dea93ed](https://git.ole-hartwig.eu/pinup/pinup/commit/dea93eda0d01b51efbaa94e3543289d87a73ed45)), closes [#ref](https://git.ole-hartwig.eu/pinup/pinup/issues/ref)
* **config:** resolve extends with provenance; dockerfile reports FROM only ([b7bd00c](https://git.ole-hartwig.eu/pinup/pinup/commit/b7bd00c5626e78661c41b789947ea11f0cf670cd))
* **datasource:** docker registry and github releases/tags datasources ([51e6296](https://git.ole-hartwig.eu/pinup/pinup/commit/51e6296eb37dd717cff2d0647ea9dac83389c4a9))
* **datasource:** go modules through a module proxy, golang-version from go.dev ([fedc58d](https://git.ole-hartwig.eu/pinup/pinup/commit/fedc58d7dd41bd2f20cd03443450f721246e0057))
* **datasource:** native apk datasource and generic custom datasources ([7c6b714](https://git.ole-hartwig.eu/pinup/pinup/commit/7c6b71487b9a182a9a6a775f11469f668e0cebef))
* **datasource:** terraform-provider and terraform-module ([b7ba7b6](https://git.ole-hartwig.eu/pinup/pinup/commit/b7ba7b6b2bdabcf3d084da17e32a5cf8030c10ce))
* **discover:** decide which files each manager sees ([152613f](https://git.ole-hartwig.eu/pinup/pinup/commit/152613fa6c225c450f41994745cd4d1eec1fa1a5))
* **extract:** declare the Manager interface and the extraction stage ([d496c89](https://git.ole-hartwig.eu/pinup/pinup/commit/d496c89e152bb08331f66c9059f53a36c7a4cca3))
* **git:** git wrapper with adopt, force-with-lease and the identity check ([749acfd](https://git.ole-hartwig.eu/pinup/pinup/commit/749acfd14087417b64d808462abdca95a262a36a))
* **glob:** add the minimatch subset the config actually uses ([46cd5b7](https://git.ole-hartwig.eu/pinup/pinup/commit/46cd5b733fbbf7ee2ffa9c0606014c275cae59a6))
* gomod manager, go-mod-directive versioning, helm and git-tags datasources ([b643068](https://git.ole-hartwig.eu/pinup/pinup/commit/b643068ec2a48c1dead342af5ab2b2b8abfe337c))
* **harness:** add the assertion interface and the refusing transport ([a96985d](https://git.ole-hartwig.eu/pinup/pinup/commit/a96985d4f4e6e84407081ac0f47e8f4d31e538de))
* **hbs:** add the handlebars subset with a tri-state environment ([1ad9e9c](https://git.ole-hartwig.eu/pinup/pinup/commit/1ad9e9c8897d69e5d1e4f99666abac16039c2f21)), closes [#if](https://git.ole-hartwig.eu/pinup/pinup/issues/if) [#if](https://git.ole-hartwig.eu/pinup/pinup/issues/if) [#10](https://git.ole-hartwig.eu/pinup/pinup/issues/10)
* **httpx:** the HTTP client every datasource goes through ([9041819](https://git.ole-hartwig.eu/pinup/pinup/commit/904181926c93526917db778191bacd92cc34f9e1))
* **jsonata:** evaluate the transform subset the config uses ([0a50f7b](https://git.ole-hartwig.eu/pinup/pinup/commit/0a50f7bbf9f3524c7731d7ef3f53f7f732bf8786))
* **jsonc:** strip comments without moving byte offsets ([2821556](https://git.ole-hartwig.eu/pinup/pinup/commit/28215567757721d5ee5e59d86608a051becbcb1a))
* **lookup:** cache releases with stale-while-revalidate and record first-seen ([e734b96](https://git.ole-hartwig.eu/pinup/pinup/commit/e734b96acb13ef5a5256ad4c97c5ed40aca2cb90))
* **lookup:** lookup stage and gitlab tags/releases/packages datasource ([1ab9295](https://git.ole-hartwig.eu/pinup/pinup/commit/1ab92956d375b03ee2e05c172732954e6140654d))
* **manager,datasource:** composer and npm, with their registries and locks ([7ed1a97](https://git.ole-hartwig.eu/pinup/pinup/commit/7ed1a9740a843e6a75cb67c63091aad2b2a7ea6d))
* **manager/dockerfile:** extract and rewrite image references ([021edcb](https://git.ole-hartwig.eu/pinup/pinup/commit/021edcb4741cb7ea94888a56bb570e02ac55853b))
* **manager/gitlabci:** image and component references from .gitlab-ci.yml ([3584b97](https://git.ole-hartwig.eu/pinup/pinup/commit/3584b978d92d76e56105122f8e1d6f55a4848b11))
* **manager/regexm:** the configured manager that carries most of the estate ([7ff7090](https://git.ole-hartwig.eu/pinup/pinup/commit/7ff7090427a618f0fac3f6d6b6820ba6cca3bd8b)), closes [14/#15](https://git.ole-hartwig.eu/14/pinup/issues/15) [16/#17](https://git.ole-hartwig.eu/16/pinup/issues/17) [21/#22](https://git.ole-hartwig.eu/21/pinup/issues/22)
* **manager:** terraform - providers, required_version, legacy providers, modules ([b389309](https://git.ole-hartwig.eu/pinup/pinup/commit/b389309727568348e795c87c47892521171280e6))
* **manager:** terraform-version ([0b1a991](https://git.ole-hartwig.eu/pinup/pinup/commit/0b1a991b388563aaec5d177157c3dd91afba75df))
* **migrate:** classify every configuration key; ignoreDeps, allowedVersions ([e4cfa3c](https://git.ole-hartwig.eu/pinup/pinup/commit/e4cfa3cd63440424b17ef930d983c4d1ec7b7d23))
* **model:** add the plan schema and its canonical serialisation ([4ea89c6](https://git.ole-hartwig.eu/pinup/pinup/commit/4ea89c6736b1386501b24bcb139a6827d7e90cd2))
* **notify:** the estate-wide rolling-major notice in one issue (D.12) ([dbf9003](https://git.ole-hartwig.eu/pinup/pinup/commit/dbf900339eb7e4af3f4f3819d47c4232eee197b2))
* **osv:** vulnerability alerts - advisories from OSV drive the fast path ([d8a785e](https://git.ole-hartwig.eu/pinup/pinup/commit/d8a785eb636e113805ef102dc19cd9d3cb620406))
* pinup's own configuration, gitlabci-include covered by gitlabci ([d075622](https://git.ole-hartwig.eu/pinup/pinup/commit/d0756220ca55ebc99ef7faf23e09551efa11ad7a))
* **planner:** a newer major on a bare-major pin is majorAvailable, held as rollingMajor ([7452033](https://git.ole-hartwig.eu/pinup/pinup/commit/7452033783c40217d6ac895d003044815a7ca0b6))
* **planner:** decide holds - minimumReleaseAge, schedule, approval, disabled ([02bfcd6](https://git.ole-hartwig.eu/pinup/pinup/commit/02bfcd622b5636bfd780a0540eebf07ac21140c2))
* **planner:** deprecated releases are not candidates (ignoreDeprecated default) ([5e2cfee](https://git.ole-hartwig.eu/pinup/pinup/commit/5e2cfeec7f8ff47c4021f20a55d53cb2df83cfc1))
* **planner:** name branches and title merge requests as Renovate does ([c669770](https://git.ole-hartwig.eu/pinup/pinup/commit/c669770041ca307f8582cd5ab75fb22dde751321))
* **planner:** plan lock-file maintenance, held until a plugin can do it ([e012ca1](https://git.ole-hartwig.eu/pinup/pinup/commit/e012ca1cb949140367f45a713204d4226495fac0))
* **planner:** turn releases into updates and wire lookup into whatif ([37fd7ec](https://git.ole-hartwig.eu/pinup/pinup/commit/37fd7ec3be63860ae4ddda7b5a3b6d9686ae0982))
* **planner:** vulnerability fast path - the lowest fix at or above the bound, under vulnerabilityAlerts ([75de45e](https://git.ole-hartwig.eu/pinup/pinup/commit/75de45e38afd125ef6432982a0ddaab220d9d0b2))
* **plugin:** tasks - lock refresh and postUpgradeTasks, compiled, allowlisted, scoped, run ([48f54df](https://git.ole-hartwig.eu/pinup/pinup/commit/48f54df1289b9fdf20c61bb35038a35a327a42dd))
* **preset:** keep inert presets for rule numbering, record provenance ([5be499f](https://git.ole-hartwig.eu/pinup/pinup/commit/5be499f1fb1d9d647411730b03473c1931200e84))
* **preset:** rebuild the preset library from captured behaviour and resolve extends ([768a0a5](https://git.ole-hartwig.eu/pinup/pinup/commit/768a0a5cc833652c311131a825edab532c4f356c))
* **print-config:** flattened paths, --explain, --diff, and the parity gate ([b8636f2](https://git.ole-hartwig.eu/pinup/pinup/commit/b8636f2ae32ee217b0341673b107edb075b15599))
* **re2x:** tell a group that matched empty from one that did not participate ([92e7de2](https://git.ole-hartwig.eu/pinup/pinup/commit/92e7de26c3240a48eb50d16e82010923dae09b01))
* **regexm:** read `# pinup:` as the same annotation marker as `# renovate:` ([15e6353](https://git.ole-hartwig.eu/pinup/pinup/commit/15e6353ee80567cf426f7f144faac4d93220e4dd))
* **rules:** matchSourceUrls fires on the URL the lookup reported ([3ff0e4f](https://git.ole-hartwig.eu/pinup/pinup/commit/3ff0e4f0e9fe1d8914af3921e408c6bb69ea5862))
* **rules:** packageRules engine, 3150 of 3150 vectors resolved as Renovate did ([047d5bd](https://git.ole-hartwig.eu/pinup/pinup/commit/047d5bd35bacc35d2b2902d33a9d83aec10c0538))
* **run:** --base, task-only branches, live lock refresh proven on the fixture ([a0efaec](https://git.ole-hartwig.eu/pinup/pinup/commit/a0efaec54b1284396cd4747c39963abe7fab9643))
* **run:** autodiscover projects by glob, one report per project ([a31e980](https://git.ole-hartwig.eu/pinup/pinup/commit/a31e9800f65aea01c853021d7047700cf1c623b9))
* **runner:** execute a plan - rebuild branch, commit, push, merge request ([75cece0](https://git.ole-hartwig.eu/pinup/pinup/commit/75cece00a30c209ec85948edd6f9de8aa6509004))
* **runner:** prConcurrentLimit, counted against what is already open ([80d594b](https://git.ole-hartwig.eu/pinup/pinup/commit/80d594b8f7ad846539eb69a36bed0ac4a786ae8f))
* **run:** plan, push, and open merge requests on GitLab ([d0a201a](https://git.ole-hartwig.eu/pinup/pinup/commit/d0a201aceb7bb8a1309818d1d1dc46c3f28342c1))
* **run:** the release fast lane - consumer index, --released, debounce ([e0c8834](https://git.ole-hartwig.eu/pinup/pinup/commit/e0c88341adad19c04cdd1c160767232c181a4697))
* **sched:** add both schedule grammars ([479d170](https://git.ole-hartwig.eu/pinup/pinup/commit/479d1705719b6ca44907f89b5cf6caa1423a668e))
* **shadow:** the comparator - plans against Renovate's open merge requests ([f348205](https://git.ole-hartwig.eu/pinup/pinup/commit/f348205c0aa4c4be63d078367b1b511e4eb61dfd))
* **versioning:** add the apk scheme, and record the agreed trigger contract ([078ac4b](https://git.ole-hartwig.eu/pinup/pinup/commit/078ac4bdf9393cc8b5f4920874b7ecf8e795a5b1))
* **versioning:** add the composer scheme ([3e0a162](https://git.ole-hartwig.eu/pinup/pinup/commit/3e0a1620eb0e142d95ebcbef133287a56bd666cb))
* **versioning:** add the interface, a conformance runner and the semver family ([2951ac0](https://git.ole-hartwig.eu/pinup/pinup/commit/2951ac02e296503ef56a4cdb63930721b39e5141))
* **versioning:** complete all eleven schemes ([589e141](https://git.ole-hartwig.eu/pinup/pinup/commit/589e14176a341874be062bef9a1f87c8390128ea))
* **versioning:** measure isCompatible and isVersion, add both to the interface ([a7a40b0](https://git.ole-hartwig.eu/pinup/pinup/commit/a7a40b03b8b5bcdbe296e0f7ed22296c58d10e57))
* **whatif:** apply packageRules before lookup and per update ([53e40fe](https://git.ole-hartwig.eu/pinup/pinup/commit/53e40fe8d8c4f037e64e0203fe80a39319dc9e1b))
* **yamlx:** confine yaml.v3 to a single package ([ec530d9](https://git.ole-hartwig.eu/pinup/pinup/commit/ec530d9588aa97030e738983beca4179dde95b1f))

### :bug: Fixes

* **extract:** dependencies that need github.com are looked up, as the runner does ([d7fe5ae](https://git.ole-hartwig.eu/pinup/pinup/commit/d7fe5aeee92ee25cc7aa7194e2a065332e655fbc))
* **lint:** wire sits below cmd rather than beside it ([093f3df](https://git.ole-hartwig.eu/pinup/pinup/commit/093f3df0dcce472187a39c5eee6adf749fe86819))
* **model:** a branch names its update, not its dependency; Markdown rendering of plans ([ddb76cd](https://git.ole-hartwig.eu/pinup/pinup/commit/ddb76cdd9395aacfa42bad1a29f73c38dc83047e))
* **planner:** a digest-pinned reference moves value and digest together, or not at all ([a1a263e](https://git.ole-hartwig.eu/pinup/pinup/commit/a1a263e9e725505b24336924cd14e5082dab266a))
* **report:** a block's note renders as code - a cron expression's asterisks are not emphasis ([dc58e62](https://git.ole-hartwig.eu/pinup/pinup/commit/dc58e62e057b4b06c53c5c1d678a464aca912399))
* **report:** table cells survive newlines and pipes; no inline HTML; one trailing newline ([e485eec](https://git.ole-hartwig.eu/pinup/pinup/commit/e485eecb59170e59f705c2f2575f76e3af1877aa))
* **spec:** the docker compatibility segment does order, in reverse ([22fb319](https://git.ole-hartwig.eu/pinup/pinup/commit/22fb3197e993327e6d6af7119a9f7d3723f3f28f))
* **testdata:** capture default.json from the authoritative clone ([cd14ec7](https://git.ole-hartwig.eu/pinup/pinup/commit/cd14ec7597c17af58fe07a03eaf26a05a018008e))

### :memo: Documentation

* close the Renovate licence question ([92c8981](https://git.ole-hartwig.eu/pinup/pinup/commit/92c8981ccd94f855d146343cbcebe537e9ab2528))
* escape a pipe in a table cell, add the synthetic corpus heading ([08ecb05](https://git.ole-hartwig.eu/pinup/pinup/commit/08ecb053b2a18049d90892f78ff239de2d83df33))
* record what building a golden image actually costs ([c58502d](https://git.ole-hartwig.eu/pinup/pinup/commit/c58502d61e2173b3a395d7e4c7a81117131e8526))
* satisfy lint:markdown ([f3f326b](https://git.ole-hartwig.eu/pinup/pinup/commit/f3f326bfe7c79b34124aa25b745433f74ca14ee1))
* **tasks:** close H.4, D.1, D.10; defer H.6 ([c4fa553](https://git.ole-hartwig.eu/pinup/pinup/commit/c4fa55385fa4d8201183a779271dea4ac31fba5e))
* **tasks:** D.12 complete ([777dff8](https://git.ole-hartwig.eu/pinup/pinup/commit/777dff8a4fd4e838660082fa881f377b04838f8d))
* **tasks:** fast path complete, own configuration ([e50fa20](https://git.ole-hartwig.eu/pinup/pinup/commit/e50fa200cd48ff64e13f57fc43b19b5791158e54))
* **tasks:** golden repositories ([d15969f](https://git.ole-hartwig.eu/pinup/pinup/commit/d15969fca8a8fc42438e745f1e82f4df86e28d1e))
* **tasks:** gomod, helm/git-tags, JSON5, fast-path planner ([9faa12d](https://git.ole-hartwig.eu/pinup/pinup/commit/9faa12dea9b9fef45e1b0553165b8ef4d4ef4305))
* **tasks:** lock refresh proven live ([64049b5](https://git.ole-hartwig.eu/pinup/pinup/commit/64049b537ba2e2877438f7b29a50c8120ce3d46f))
* **tasks:** mutation suite, renderer, key defect ([2186d8c](https://git.ole-hartwig.eu/pinup/pinup/commit/2186d8cc555877bf73955fa521c256ca9d4dae80))
* **tasks:** plugin system, live defects ([8412040](https://git.ole-hartwig.eu/pinup/pinup/commit/84120404ada3a5779883ed3207a662fdc4fe0bc2))
* **tasks:** record cache wiring and the hold policy ([eec06f9](https://git.ole-hartwig.eu/pinup/pinup/commit/eec06f9b8a05cc9dba11782be109328f8e6831a2))
* **tasks:** record composer/npm, throttling, migrate, the component ([431aaa5](https://git.ole-hartwig.eu/pinup/pinup/commit/431aaa5dfc92f3a6b9e1cb1b581bcec1949d9fa1))
* **tasks:** record defaults, migrations and branch naming ([ba1d593](https://git.ole-hartwig.eu/pinup/pinup/commit/ba1d593cdfc7da89c3dc635f8641dba65876dff1))
* **tasks:** record H.1, P0.13 and the rules wiring ([eb745b8](https://git.ole-hartwig.eu/pinup/pinup/commit/eb745b84323e4e1e45af4a9807cad3d6a5047622))
* **tasks:** record lookup, gitlabds, planner and the live ci-tools run ([17015b1](https://git.ole-hartwig.eu/pinup/pinup/commit/17015b1e9a83db493ba032f23dc3a06a4abcd26e))
* **tasks:** record print-config ([d080bd8](https://git.ole-hartwig.eu/pinup/pinup/commit/d080bd877cb45b4173ee26350568d866ee9bd59b))
* **tasks:** record repository configuration ([8ba4739](https://git.ole-hartwig.eu/pinup/pinup/commit/8ba47394a9006048945a24f9f4a43178c7d02814))
* **tasks:** record terraform manager, annotation prefix, matchSourceUrls ([63dbee2](https://git.ole-hartwig.eu/pinup/pinup/commit/63dbee2442db04e6a2ad6e33321a302aabb319cc))
* **tasks:** record the apk and custom datasources ([29592e7](https://git.ole-hartwig.eu/pinup/pinup/commit/29592e730557db644b6fb524108e1c7377166e20))
* **tasks:** record the applier ([c43e3d9](https://git.ole-hartwig.eu/pinup/pinup/commit/c43e3d9d45776d0c8fec9bb37b3288e857dafb90))
* **tasks:** record the delivery work and the credential gate ([c345025](https://git.ole-hartwig.eu/pinup/pinup/commit/c3450251b580c916c3f32ab35198d20970f11cfe))
* **tasks:** record the digest defect and its fix ([6f0c8f3](https://git.ole-hartwig.eu/pinup/pinup/commit/6f0c8f3a6e5cf38a00231d43b0df1df408d287e2))
* **tasks:** record the fast lane ([f6e79c1](https://git.ole-hartwig.eu/pinup/pinup/commit/f6e79c1e39ee3b34f8b5e2b0225ace13ce527ea1))
* **tasks:** record the platform layer and the first live run ([3082a3f](https://git.ole-hartwig.eu/pinup/pinup/commit/3082a3f4d561f98aa13e0e0b07eed08c3c5ab928))
* **tasks:** record the preset rebuild and the two datasources ([a95e012](https://git.ole-hartwig.eu/pinup/pinup/commit/a95e0122600c28207f597c859d5c622ce177ab6a))
* **tasks:** record the shadow comparator ([94cdf10](https://git.ole-hartwig.eu/pinup/pinup/commit/94cdf105dcf5c030c4e02016c928ded60334d8d0))
* **tasks:** terraform datasources, kustomize, rolling major, digest gap ([7b9a8b6](https://git.ole-hartwig.eu/pinup/pinup/commit/7b9a8b622bac20c3603590fcc6610fdb7c088f0c))

### :white_check_mark: Tests

* capture default.json as the acceptance surface ([be22f63](https://git.ole-hartwig.eu/pinup/pinup/commit/be22f63bac324c01e296673a097f9d071274493a))
* **cmd:** the askpass fallback test clears the group's GITLAB_TOKEN, present in every CI job now ([85962c8](https://git.ole-hartwig.eu/pinup/pinup/commit/85962c83bf15a2d17cdc119768f0a6a757bbaf4f))
* **corpus:** gomod vectors and the go-mod-directive versioning table ([6ef9af1](https://git.ole-hartwig.eu/pinup/pinup/commit/6ef9af12a7630772545a0c857a41aea059f262fb))
* **corpus:** synthetic tree for terraform-version and kustomize (14 vectors) ([f19c848](https://git.ole-hartwig.eu/pinup/pinup/commit/f19c848571c824c9bf0db0f5497e360a81c25f88))
* **golden:** five golden repositories, recorded live and replayed byte for byte (H.5) ([c78154c](https://git.ole-hartwig.eu/pinup/pinup/commit/c78154cb8708e40d9c5d8f89b64b9d886b602aff))
* **lint:** add the convention tests for rules no linter knows ([9cfd142](https://git.ole-hartwig.eu/pinup/pinup/commit/9cfd142c4f4f1c54428ea26527289f3be8c203f2))
* **mutation:** a target whose tests all skipped is an untested gate, not a passed one ([64e502c](https://git.ole-hartwig.eu/pinup/pinup/commit/64e502cf841609f1806181c7c9bcbb1a0ca38954))
* **mutation:** copy the tree without git; the CI image carries none ([bec1be9](https://git.ole-hartwig.eu/pinup/pinup/commit/bec1be955072de76e912f71bdcbac466e460d783))
* **mutation:** twenty-two named breakages, each seen red by its layer (H.7) ([1f95bc6](https://git.ole-hartwig.eu/pinup/pinup/commit/1f95bc62c9250ca1408b5143d52b688d8a110b19))
* **parity:** record the measured vulnerability-alert behaviour ([40dd0e3](https://git.ole-hartwig.eu/pinup/pinup/commit/40dd0e3a64f07023b6331862c7fd77bab8e5e2c4))
* **runner:** a lock lands with its edit or not at all; scope and toolchain refusals ([0c34b7e](https://git.ole-hartwig.eu/pinup/pinup/commit/0c34b7e48b4065b1f9148941128a319e3ccb8467))

### :repeat: Continuous Integrations

* check the resolved job list with a Go tool instead of curl and jq ([a4abc8f](https://git.ole-hartwig.eu/pinup/pinup/commit/a4abc8f31a12696116f0b6a9fa5a026a7869d000))
* drop the unused curl image variable ([091f0fa](https://git.ole-hartwig.eu/pinup/pinup/commit/091f0faf71babeb03191c994eb6720f61ce587f4))
* publish the bare binaries beside the zips, for the image ([ab9392d](https://git.ole-hartwig.eu/pinup/pinup/commit/ab9392dd76fc8c068526a050968e2c548b569acc))
* read the created job list from the running pipeline ([34cc228](https://git.ole-hartwig.eu/pinup/pinup/commit/34cc22846c54aafb6e17cf6e47c948e63fb0d6d5))
* release path - semantic-release, manifest, upload, links ([d6af438](https://git.ole-hartwig.eu/pinup/pinup/commit/d6af43821c439410c110e895c9c47c9a2a347473))

### :repeat: Chores

* scaffold the module, the CLI surface and the pipeline ([c8ab30d](https://git.ole-hartwig.eu/pinup/pinup/commit/c8ab30de9644f9f2e458fe339fc8e165a0d4b147))
