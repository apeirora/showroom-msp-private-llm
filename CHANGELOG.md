# Changelog

## [2.9.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.8.2...v2.9.0) (2026-04-21)


### Features

* **charts:** add private-llm-msp-app and private-llm-pm-app umbrellas ([#117](https://github.com/apeirora/showroom-msp-private-llm/issues/117)) ([43fd7e0](https://github.com/apeirora/showroom-msp-private-llm/commit/43fd7e0afa778a19944f1ce669ff57302bb6f913))


### Bug Fixes

* **sync-agent:** bump api-syncagent to 0.5.1 for hostAliases support ([#111](https://github.com/apeirora/showroom-msp-private-llm/issues/111)) ([f89cc22](https://github.com/apeirora/showroom-msp-private-llm/commit/f89cc221111e4294cce59631d3be8e5e81708fa8))
* **sync-agent:** fix hostAliases format and add apiExportEndpointSliceName ([#113](https://github.com/apeirora/showroom-msp-private-llm/issues/113)) ([3e54f8c](https://github.com/apeirora/showroom-msp-private-llm/commit/3e54f8c9aeeec9870e8b113e7e1ec001733302cf))

## [2.8.2](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.8.1...v2.8.2) (2026-03-24)


### Bug Fixes

* golint formatting ([#107](https://github.com/apeirora/showroom-msp-private-llm/issues/107)) ([0d51107](https://github.com/apeirora/showroom-msp-private-llm/commit/0d51107c142f140ee572b59c59b2ab4ee221f310))
* make OCM chart compatible with controller v0.29.0 ([29f8809](https://github.com/apeirora/showroom-msp-private-llm/commit/29f8809ed343bccf862683e36207c351667a6897))
* **metadata:** remove tracked email placeholders ([#104](https://github.com/apeirora/showroom-msp-private-llm/issues/104)) ([c9c64d0](https://github.com/apeirora/showroom-msp-private-llm/commit/c9c64d05639310c6d8e88e5cea3a33c248752dbd))
* OCM chart consistency improvements ([6acbe0f](https://github.com/apeirora/showroom-msp-private-llm/commit/6acbe0fa9d50b418850f173e3a0d884466850ce6))
* replace KRO with plain OCM resources (tested on cluster) ([80105dd](https://github.com/apeirora/showroom-msp-private-llm/commit/80105ddaf810dfaa66e64fc96bd56f083065035c))
* update kube-rbac-proxy image to registry.k8s.io ([bc48695](https://github.com/apeirora/showroom-msp-private-llm/commit/bc48695bce697c963c1b4e3f4ea5ae9ed3ee5ed7))
* update kube-rbac-proxy image to registry.k8s.io ([abd10ad](https://github.com/apeirora/showroom-msp-private-llm/commit/abd10ad0c4593ff7f4fcb664b56614f2f5d9ccd9))
* use quay.io/brancz/kube-rbac-proxy and make image configurable ([df4962d](https://github.com/apeirora/showroom-msp-private-llm/commit/df4962d3ceabf029a73bc42a4be3353c451d1e87))

## [2.8.1](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.8.0...v2.8.1) (2026-03-06)


### Bug Fixes

* **chart:** Roll portal integration on content changes ([#94](https://github.com/apeirora/showroom-msp-private-llm/issues/94)) ([6a5c13e](https://github.com/apeirora/showroom-msp-private-llm/commit/6a5c13efcbce69ac504c54722ebd961a7d019fe4))
* update Go module path from example to actual repository ([#91](https://github.com/apeirora/showroom-msp-private-llm/issues/91)) ([d248e5c](https://github.com/apeirora/showroom-msp-private-llm/commit/d248e5c1dd9b6280e17ad65082eb595bcb1c5e51))
* update remaining example references in PROJECT and e2e test ([#93](https://github.com/apeirora/showroom-msp-private-llm/issues/93)) ([48de2d6](https://github.com/apeirora/showroom-msp-private-llm/commit/48de2d69ca4c6b484c996dcfa547759b086d9068))

## [2.8.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.7.0...v2.8.0) (2026-03-03)


### Features

* **chart:** make replicas field required ([#89](https://github.com/apeirora/showroom-msp-private-llm/issues/89)) ([c08abd7](https://github.com/apeirora/showroom-msp-private-llm/commit/c08abd79588d048ee0bd5237268263916d284140))

## [2.7.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.10...v2.7.0) (2026-03-03)


### Features

* **ui:** replace replicas text input with dropdown selector ([#87](https://github.com/apeirora/showroom-msp-private-llm/issues/87)) ([2cfbdc1](https://github.com/apeirora/showroom-msp-private-llm/commit/2cfbdc18d6885a0e2fb6e9680cff8b3a0f1eeea0))

## [2.6.10](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.9...v2.6.10) (2026-03-02)


### Bug Fixes

* touch APITokenRequest when secret is updated ([#85](https://github.com/apeirora/showroom-msp-private-llm/issues/85)) ([f6b1c3d](https://github.com/apeirora/showroom-msp-private-llm/commit/f6b1c3dc55feeb78448faa12856e020464653c7e))

## [2.6.9](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.8...v2.6.9) (2026-02-26)


### Bug Fixes

* **controller:** Gate private LLM readiness on deployment and service health ([#83](https://github.com/apeirora/showroom-msp-private-llm/issues/83)) ([9e1509b](https://github.com/apeirora/showroom-msp-private-llm/commit/9e1509bc5524122421f819812c64dee477fb3165))

## [2.6.8](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.7...v2.6.8) (2026-02-18)


### Bug Fixes

* add required verbs to v1alpha2 permissionClaims ([#81](https://github.com/apeirora/showroom-msp-private-llm/issues/81)) ([979e198](https://github.com/apeirora/showroom-msp-private-llm/commit/979e1988b8d2a9057c5bf89cd4f55e23751560f5))
* remove v1alpha1 'all' field from permissionClaims ([#78](https://github.com/apeirora/showroom-msp-private-llm/issues/78)) ([2f14d29](https://github.com/apeirora/showroom-msp-private-llm/commit/2f14d29e55166c435122923a5224e5becfd4c082))
* update APIExport apiVersion to v1alpha2 ([#76](https://github.com/apeirora/showroom-msp-private-llm/issues/76)) ([0a8ffaa](https://github.com/apeirora/showroom-msp-private-llm/commit/0a8ffaaf532c2093d62041a55d3c9c7e0ae3b6f2))

## [2.6.7](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.6...v2.6.7) (2026-02-18)


### Bug Fixes

* **chart:** Default Secret group to v1 ([#75](https://github.com/apeirora/showroom-msp-private-llm/issues/75)) ([f614df4](https://github.com/apeirora/showroom-msp-private-llm/commit/f614df45a8b2c1a2e3ea9c61c316e078485ccc78))
* remove v1alpha1 'all' field from permissionClaims ([#78](https://github.com/apeirora/showroom-msp-private-llm/issues/78)) ([2f14d29](https://github.com/apeirora/showroom-msp-private-llm/commit/2f14d29e55166c435122923a5224e5becfd4c082))
* update APIExport apiVersion to v1alpha2 ([#76](https://github.com/apeirora/showroom-msp-private-llm/issues/76)) ([0a8ffaa](https://github.com/apeirora/showroom-msp-private-llm/commit/0a8ffaaf532c2093d62041a55d3c9c7e0ae3b6f2))

## [2.6.6](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.5...v2.6.6) (2026-02-18)


### Bug Fixes

* **chart:** Default Secret group to v1 ([#75](https://github.com/apeirora/showroom-msp-private-llm/issues/75)) ([f614df4](https://github.com/apeirora/showroom-msp-private-llm/commit/f614df45a8b2c1a2e3ea9c61c316e078485ccc78))
* **core:** Restore secrets sidebar and token request dynamic query ([46b3f0d](https://github.com/apeirora/showroom-msp-private-llm/commit/46b3f0d28ebad8c4813787460a58d2d1c4a8af10))
* **core:** Version LLM pm-content GraphQL resources ([#72](https://github.com/apeirora/showroom-msp-private-llm/issues/72)) ([d9c87fc](https://github.com/apeirora/showroom-msp-private-llm/commit/d9c87fc99e03102d30fb754e5d0a19d59cdf6acf))
* **pm-content:** parametrize secret graphql resource definition ([7b45d63](https://github.com/apeirora/showroom-msp-private-llm/commit/7b45d630b23dae3e6c2d7959e9bd0857151628eb))
* **ui:** restore secrets sidebar and fix token request dynamic query ([99f6d96](https://github.com/apeirora/showroom-msp-private-llm/commit/99f6d969f9cc2a51a03fd09cb8752d9bfecf6579))
* update APIExport apiVersion to v1alpha2 ([#76](https://github.com/apeirora/showroom-msp-private-llm/issues/76)) ([0a8ffaa](https://github.com/apeirora/showroom-msp-private-llm/commit/0a8ffaaf532c2093d62041a55d3c9c7e0ae3b6f2))

## [2.6.5](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.4...v2.6.5) (2026-02-16)


### Bug Fixes

* change default kcpKubeconfig to pm-kubeconfig ([#69](https://github.com/apeirora/showroom-msp-private-llm/issues/69)) ([bb670fc](https://github.com/apeirora/showroom-msp-private-llm/commit/bb670fc1edae61730acb734daf3c439bec52dac8))
* **controller:** Gate token secret until instance endpoint is ready ([#71](https://github.com/apeirora/showroom-msp-private-llm/issues/71)) ([e893c94](https://github.com/apeirora/showroom-msp-private-llm/commit/e893c945954ee7a680729e930eaafe1e2da7beab))

## [2.6.4](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.3...v2.6.4) (2026-02-15)


### Bug Fixes

* **ui:** simplify LLM provider list view columns ([#67](https://github.com/apeirora/showroom-msp-private-llm/issues/67)) ([b794e98](https://github.com/apeirora/showroom-msp-private-llm/commit/b794e985fa104c862f4f04b1b6e35e2d500b3dfc))

## [2.6.3](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.2...v2.6.3) (2026-01-29)


### Bug Fixes

* add ghcr.io authentication for OCM component build ([#65](https://github.com/apeirora/showroom-msp-private-llm/issues/65)) ([b77ebec](https://github.com/apeirora/showroom-msp-private-llm/commit/b77ebec26a30abdbb4d44748ce362904918154ce))

## [2.6.2](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.1...v2.6.2) (2026-01-29)


### Bug Fixes

* **pm-integration:** remove unnecessary postgresql RBAC ([507a613](https://github.com/apeirora/showroom-msp-private-llm/commit/507a61321fb95db1c5ebacf58ee5971cc55bb394))

## [2.6.1](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.6.0...v2.6.1) (2026-01-29)


### Bug Fixes

* **pm-integration:** add RBAC for sync-agent virtual workspace access ([3f0e531](https://github.com/apeirora/showroom-msp-private-llm/commit/3f0e531544dacf180f93e301d3b7dcffb46e2191))

## [2.6.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.5.2...v2.6.0) (2025-11-26)


### Features

* add RBAC configuration for apiexport binding and anonymous user access ([#61](https://github.com/apeirora/showroom-msp-private-llm/issues/61)) ([3e8e942](https://github.com/apeirora/showroom-msp-private-llm/commit/3e8e9425bdea3a6ce0c29cec703b416866be550c))

## [2.5.2](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.5.1...v2.5.2) (2025-11-24)


### Bug Fixes

* correct variable replacement order in portal integration configmap ([8d7f5f6](https://github.com/apeirora/showroom-msp-private-llm/commit/8d7f5f64ab7cebb31f90d03f64814221c6ebc73c))

## [2.5.1](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.5.0...v2.5.1) (2025-11-24)


### Bug Fixes

* build workflow env reference ([#58](https://github.com/apeirora/showroom-msp-private-llm/issues/58)) ([bbab606](https://github.com/apeirora/showroom-msp-private-llm/commit/bbab60698077863b51df5e7f55dd92e02456ccfe))

## [2.5.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.4.1...v2.5.0) (2025-11-24)


### Features

* add UI settings for OPENAI_API_KEY to enhance user experience with secret display and copy functionality ([#54](https://github.com/apeirora/showroom-msp-private-llm/issues/54)) ([17efe42](https://github.com/apeirora/showroom-msp-private-llm/commit/17efe422861c9edebc9e2ee684f2993fe8b31f0f))
* sidebar title dynamic labeling ([#56](https://github.com/apeirora/showroom-msp-private-llm/issues/56)) ([eb7d86b](https://github.com/apeirora/showroom-msp-private-llm/commit/eb7d86bb2945de39de16acfd008d0fcc925cb254))

## [2.4.1](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.4.0...v2.4.1) (2025-11-19)


### Bug Fixes

* update chart version ([#51](https://github.com/apeirora/showroom-msp-private-llm/issues/51)) ([0e7101a](https://github.com/apeirora/showroom-msp-private-llm/commit/0e7101a0b9309c2a16dd6462d8f99cd8943923e1))

## [2.4.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.3.1...v2.4.0) (2025-11-17)


### Features

* **controller:** update API token handling to include OPENAI_API_URL alongside OPENAI_API_KEY in secrets and documentation ([#49](https://github.com/apeirora/showroom-msp-private-llm/issues/49)) ([4a9ad38](https://github.com/apeirora/showroom-msp-private-llm/commit/4a9ad382899f10ec7ea2f68d57b286a2d69808ad))

## [2.3.1](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.3.0...v2.3.1) (2025-11-13)


### Bug Fixes

* update llama.cpp server image tag to version b7045 ([#47](https://github.com/apeirora/showroom-msp-private-llm/issues/47)) ([b7ae46c](https://github.com/apeirora/showroom-msp-private-llm/commit/b7ae46c467c604fa9c13ec1854fd528744c1eedc))

## [2.3.0](https://github.com/apeirora/showroom-msp-private-llm/compare/v2.2.1...v2.3.0) (2025-11-13)


### Features

* auto-update appVersion and image tag in Helm chart and values files ([#46](https://github.com/apeirora/showroom-msp-private-llm/issues/46)) ([a594373](https://github.com/apeirora/showroom-msp-private-llm/commit/a594373f745ba7b48aef433ef1d97213859c47f8))
* update labels and descriptions to reflect MODEL Corp branding in JSON and YAML files ([#45](https://github.com/apeirora/showroom-msp-private-llm/issues/45)) ([3a9a720](https://github.com/apeirora/showroom-msp-private-llm/commit/3a9a720958a73a516538cbfa165971d4165604bb))


### Bug Fixes

* enhance ingress configuration and permissions in portal integration ([#44](https://github.com/apeirora/showroom-msp-private-llm/issues/44)) ([b9ad377](https://github.com/apeirora/showroom-msp-private-llm/commit/b9ad3775089504b492db116c0c70127fffcaa829))
* update labels in UI and add optional deployment instructions in documentation ([#42](https://github.com/apeirora/showroom-msp-private-llm/issues/42)) ([6079d37](https://github.com/apeirora/showroom-msp-private-llm/commit/6079d378a0c000434ef96fd2d2b225dd6ad10125))

## [2.2.1](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v2.2.0...v2.2.1) (2025-10-30)


### Bug Fixes

* deployment configs for ocm do not upgrade operator ([#40](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/40)) ([a55e626](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/a55e6266b75092009ecc0c384510b5e16fb2b705))

## [2.2.0](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v2.1.0...v2.2.0) (2025-10-30)


### Features

* added gemma models to the operator ([#36](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/36)) ([9adde76](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/9adde76cbd3a2ff0eb775b21a88f69d214d4be9f))


### Bug Fixes

* portal integration after testing ([#38](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/38)) ([c579fe2](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/c579fe2b328d32faf3e8f63f3d56c84c747bf04a))

## [2.1.0](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v2.0.1...v2.1.0) (2025-10-29)


### Features

* add portal integration for serving static content and applying configurations ([#35](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/35)) ([b9416be](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/b9416be1e9e3aa9007f1c05b0f33537541e8d63e))

## [2.0.1](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v2.0.0...v2.0.1) (2025-10-27)


### Bug Fixes

* update variable name from TokenRequest to APITokenRequest ([#32](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/32)) ([e048849](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/e0488494e3cc01a8b39e86d90a3d19a8cc88d42c))

## [2.0.0](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.5...v2.0.0) (2025-10-27)


### ⚠ BREAKING CHANGES

* rename TokenRequest to APITokenRequest and update related documentation and resources ([#30](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/30))

### Features

* add model validation and default value for LLMInstance CRD ([#27](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/27)) ([541e8f1](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/541e8f13df75959b8409def9674ce62985622222))
* make finalizer non-blocking ([#29](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/29)) ([709c1f7](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/709c1f78032d21e45411bc1c3777cb7a2aba8c97))
* set TokenRequest's status.phase and improve finalizer handling ([#28](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/28)) ([d71290e](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/d71290e6137d5ed04117885b09adc842a9b45048))


### Bug Fixes

* update LLM instance path and endpoint to be shorter ([#26](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/26)) ([37f413a](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/37f413a4c1234f731b538384d4b31e2d804dcbef))


### Code Refactoring

* rename TokenRequest to APITokenRequest and update related documentation and resources ([#30](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/30)) ([8044d53](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/8044d535119a319f3f2d11cfd0da9b1e5cad9087))

## [1.0.5](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.4...v1.0.5) (2025-10-21)


### Bug Fixes

* trigger release ([f070b0a](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/f070b0a001d11ab4e97744471834c4cade2bb11b))

## [1.0.4](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.3...v1.0.4) (2025-10-20)


### Bug Fixes

* remove rgd from ocm ([d83d27d](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/d83d27d7d6af98a292f4b576c537c5a6fb719d1b))

## [1.0.3](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.2...v1.0.3) (2025-10-17)


### Bug Fixes

* integer in rgd for llm ([07418e4](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/07418e40734e70d493bc1dae338010ebf6d3a678))
* slug generation ([d0fff87](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/d0fff87a2503955fe7344a409a33d2249614b289))

## [1.0.2](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.1...v1.0.2) (2025-10-16)


### Bug Fixes

* slug generation ([76bdf04](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/76bdf04450de1f720810d6c1bc7301ad4bdd9c22))

## [1.0.1](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v1.0.0...v1.0.1) (2025-10-16)


### Bug Fixes

* proper ocm name ([6a7b3d5](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/6a7b3d5c093330424a1a589335e95376643c3115))

## [1.0.0](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v0.0.1...v1.0.0) (2025-10-16)


### ⚠ BREAKING CHANGES

* trigger release ([#15](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/15))

### Bug Fixes

* trigger release ([#15](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/15)) ([b80a160](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/b80a16053f8ed2babc7d3052270f1c724b9b12af))

## [0.0.1](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v0.0.1...v0.0.1) (2025-10-16)


### ⚠ BREAKING CHANGES

* trigger release ([#14](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/14))

### Features

* **chart:** add Traefik switcher configuration and deployment docs ([ae9d355](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae9d355f7cda8081b7cab25da5847b8558f4823f))
* initial implementation ([8e6c446](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/8e6c446f0b129eee522a6f7bb9385a3ac1e7905a))
* update release configuration and enhance GitHub Actions workflow ([ae1b12a](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae1b12ac70d1b75832f0bbf5837a49ab3074c432))


### Bug Fixes

* remove release configuration files  ([#10](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/10)) ([46fd884](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/46fd884f1d3795d808d9f8d484f645dd4da36e61))
* trigger release ([#14](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/14)) ([696e9e5](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/696e9e578c3c313afdbdf4a64950ba936bf7d6f0))


### Continuous Integration

* conventional commits ([7df8888](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/7df88886fbdb7325588a924b58f6c8b8080fbbb6))

## [0.0.1](https://github.com/apeirora/showroom-msp-private-llm-operator/compare/v0.0.1...v0.0.1) (2025-10-16)


### Features

* **chart:** add Traefik switcher configuration and deployment docs ([ae9d355](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae9d355f7cda8081b7cab25da5847b8558f4823f))
* initial implementation ([8e6c446](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/8e6c446f0b129eee522a6f7bb9385a3ac1e7905a))
* update release configuration and enhance GitHub Actions workflow ([ae1b12a](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae1b12ac70d1b75832f0bbf5837a49ab3074c432))


### Bug Fixes

* remove release configuration files  ([#10](https://github.com/apeirora/showroom-msp-private-llm-operator/issues/10)) ([46fd884](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/46fd884f1d3795d808d9f8d484f645dd4da36e61))


### Continuous Integration

* conventional commits ([7df8888](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/7df88886fbdb7325588a924b58f6c8b8080fbbb6))

## 0.0.1 (2025-10-16)


### Features

* **chart:** add Traefik switcher configuration and deployment docs ([ae9d355](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae9d355f7cda8081b7cab25da5847b8558f4823f))
* initial implementation ([8e6c446](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/8e6c446f0b129eee522a6f7bb9385a3ac1e7905a))
* update release configuration and enhance GitHub Actions workflow ([ae1b12a](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/ae1b12ac70d1b75832f0bbf5837a49ab3074c432))


### Continuous Integration

* conventional commits ([7df8888](https://github.com/apeirora/showroom-msp-private-llm-operator/commit/7df88886fbdb7325588a924b58f6c8b8080fbbb6))
