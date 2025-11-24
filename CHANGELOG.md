# Changelog

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
