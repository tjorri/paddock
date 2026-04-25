# Changelog

## [0.4.0](https://github.com/tjorri/paddock/compare/v0.3.0...v0.4.0) (2026-04-25)


### ⚠ BREAKING CHANGES

* **api,controller,broker,proxy:** bounded egress discovery window (Plan D) ([#11](https://github.com/tjorri/paddock/issues/11))
* **api,controller:** broker interception mode explicit opt-in (Plan B) ([#9](https://github.com/tjorri/paddock/issues/9))
* **api,broker,controller:** v0.4 broker secret injection core (Plan A) ([#7](https://github.com/tjorri/paddock/issues/7))

### Features

* **api,broker,controller:** v0.4 broker secret injection core (Plan A) ([#7](https://github.com/tjorri/paddock/issues/7)) ([36aca4a](https://github.com/tjorri/paddock/commit/36aca4a80b3bcb2c83ae51a1dda098139e974b44))
* **api,controller,broker,proxy:** bounded egress discovery window (Plan D) ([#11](https://github.com/tjorri/paddock/issues/11)) ([49aa6b3](https://github.com/tjorri/paddock/commit/49aa6b3f479094b78a87158b8ade94b0fa770412))
* **api,controller:** broker interception mode explicit opt-in (Plan B) ([#9](https://github.com/tjorri/paddock/issues/9)) ([26cf2e8](https://github.com/tjorri/paddock/commit/26cf2e8c3e73ebb79cba8548d7ac56b04d071bdc))
* **cli:** paddock policy suggest + observability (Plan C) ([#10](https://github.com/tjorri/paddock/issues/10)) ([f7ec3a9](https://github.com/tjorri/paddock/commit/f7ec3a92b6cc986572724038ac761c711d574049))


### Documentation

* add v0.4 Plan A + drop v1alpha2 from spec 0003 (evolve v1alpha1 in place) ([562f452](https://github.com/tjorri/paddock/commit/562f4522d4a65d1a18a889937f43c6404de71a42))
* **plans:** add v0.4 Plan A broker secret injection core ([8aea78f](https://github.com/tjorri/paddock/commit/8aea78f2f415f8afc39c8cc4717715b41fee86e8))
* **specs:** add 0003 broker secret injection redesign (v0.4) ([e9bcc19](https://github.com/tjorri/paddock/commit/e9bcc19560646b452a6842f0881da29c5c7d39c1))
* v0.4 operator cookbooks + docs reorganization (Plan E) ([#13](https://github.com/tjorri/paddock/issues/13)) ([6f35687](https://github.com/tjorri/paddock/commit/6f35687fea7cb7ebf3c1d2bda8b91e14aeda6de1))

## [0.3.0](https://github.com/tjorri/paddock/compare/v0.2.0...v0.3.0) (2026-04-24)


### ⚠ BREAKING CHANGES

* **api:** replace template.credentials with requires block
* **api:** add BrokerPolicy + AuditEvent CRDs with webhooks and TTL reaper

### Features

* **api:** add BrokerPolicy + AuditEvent CRDs with webhooks and TTL reaper ([99ca146](https://github.com/tjorri/paddock/commit/99ca1460954ed3851018dbe84bff20d6d9cc4891))
* **api:** replace template.credentials with requires block ([279ad93](https://github.com/tjorri/paddock/commit/279ad936d514f792c23de585567a6a8ffa5dea82))
* **broker,proxy:** AnthropicAPIProvider + MITM auth substitution ([93880b7](https://github.com/tjorri/paddock/commit/93880b760630cc251146098e7df4f690a0d1d015))
* **broker:** deployable broker binary + StaticProvider + audit path ([0c45a68](https://github.com/tjorri/paddock/commit/0c45a68f7383d6b708210eead06c9ae95e0f7631))
* **broker:** GitHubAppProvider with per-run token reuse ([84a02e6](https://github.com/tjorri/paddock/commit/84a02e63e94117fd73e4ce0ba0115ded1ff00b6d))
* **broker:** PATPoolProvider — lease-from-pool gitforge credentials ([3d8c021](https://github.com/tjorri/paddock/commit/3d8c02198f273ec7ffd014fa518b8cba3df576ea))
* **cli:** policy / audit / describe subcommands ([bb04682](https://github.com/tjorri/paddock/commit/bb0468252767a25ea01b5e4754dbb2101d74f196))
* **controller:** wire admission intersection + broker credential issuance ([94d7847](https://github.com/tjorri/paddock/commit/94d7847b3db323eb9b5094db4b0d260875241b6b))
* **proxy:** cooperative-mode egress proxy sidecar + run-scoped MITM CA ([6a22ea5](https://github.com/tjorri/paddock/commit/6a22ea5bb32686c1eb3cb9e93b56250481e1b4cd))
* **proxy:** per-run NetworkPolicy layer with CNI auto-detection ([f09a40d](https://github.com/tjorri/paddock/commit/f09a40d649144a4c7450fb0ad3887cd0585dcb84))
* **proxy:** transparent-mode interception via iptables-init ([c320a4f](https://github.com/tjorri/paddock/commit/c320a4f6e41e36cffdae6bfcc9c750c06c3fb5a6))
* **workspace:** broker-backed seed credentials with proxy sidecar ([1ef5809](https://github.com/tjorri/paddock/commit/1ef58091d7bdc79935a656e51eff0ec479a3698e))


### Bug Fixes

* **broker:** drop the broken defaultMode on the TLS cert volume ([0637250](https://github.com/tjorri/paddock/commit/0637250d73086da18163247e0becaa7434b8bb59))
* **kustomize:** scope metrics + webhook patches to the controller-manager Deployment ([174b104](https://github.com/tjorri/paddock/commit/174b104bdcdd44a96f87adddd0fbd6e1a588ea4c))
* **proxy:** include internal/broker/api in the proxy image build context ([91c4d00](https://github.com/tjorri/paddock/commit/91c4d00fc731d69fe1228c2717640aa9c0591e8b))
* **rbac:** grant the manager create on auditevents to satisfy escalation checks ([a8674e7](https://github.com/tjorri/paddock/commit/a8674e71735024c5eaa69909585e3679f299ed65))
* **rbac:** grant the manager get/list/watch on namespaces ([1210422](https://github.com/tjorri/paddock/commit/1210422864c8eb9e915da5c9827bddf870e015d9))
* **rbac:** grant the manager list/watch on brokerpolicies ([bccc7a0](https://github.com/tjorri/paddock/commit/bccc7a0e5a566606868f0861e4465261c4b7c41d))


### Documentation

* spec 0002 + ADRs 0012-0016 for v0.3 broker/proxy ([11dd84b](https://github.com/tjorri/paddock/commit/11dd84b5e8714a8c1a50f23596fe1651baf63f2e))
* v0.3 refresh — README, CONTRIBUTING, chart README, ADR-0013 fix ([0254a96](https://github.com/tjorri/paddock/commit/0254a9633190effd72d2124a6b15bad463d04d7b))

## [0.2.0](https://github.com/tjorri/paddock/compare/v0.1.1...v0.2.0) (2026-04-23)


### ⚠ BREAKING CHANGES

* **workspace:** seed multiple git repos per Workspace ([#3](https://github.com/tjorri/paddock/issues/3))

### Features

* **workspace:** seed multiple git repos per Workspace ([#3](https://github.com/tjorri/paddock/issues/3)) ([8e48a55](https://github.com/tjorri/paddock/commit/8e48a55807f0362ad18b44ec3426843148f15865))

## [0.1.1](https://github.com/tjorri/paddock/compare/v0.1.0...v0.1.1) (2026-04-23)


### Features

* **controller:** graceful failure on prompt-resolution errors ([3739d19](https://github.com/tjorri/paddock/commit/3739d19c04171c4637c1d2d1d212cf455b821254))
* **controller:** materialise prompts as Secrets, not ConfigMaps ([9cbed5e](https://github.com/tjorri/paddock/commit/9cbed5e706fb2d89dde1a7c621e072d1d1d3be1c))
* **webhook:** cap inline prompt size at 256 KiB ([151f65b](https://github.com/tjorri/paddock/commit/151f65b963776d37ea962537e2f05bd736cc4d78))


### Bug Fixes

* **cli:** drop shell in logs reader pod to prevent injection ([a9ff4e2](https://github.com/tjorri/paddock/commit/a9ff4e2c27978ec8167f2299eae9ce8289c74b96))
* **controller:** harden workspace seed Job for restricted PSS ([7428515](https://github.com/tjorri/paddock/commit/74285152f37fdbb0de2fa5a39abf1098e61ea0a3))


### Documentation

* sync spec with shipped reality ([137a81d](https://github.com/tjorri/paddock/commit/137a81d2406c66d8a1bee1bc49876ec832eb9930))

## 0.1.0 (2026-04-23)


### Features

* **api:** v1alpha1 CRDs and validating webhooks (M1) ([97a2a31](https://github.com/tjorri/paddock/commit/97a2a31f1c8a0240daffc18d77bd9c2d44efe91f))
* **chart:** wire collectorImage value through to the Deployment ([4290400](https://github.com/tjorri/paddock/commit/429040048951b8cb58552602f95174ed0ab51b40))
* **cli:** events + logs subcommands (M9) ([1013db7](https://github.com/tjorri/paddock/commit/1013db73f6962a450d3b229c296f076ff5d6d0dc))
* **cli:** kubectl-paddock v0 — submit, status, list, cancel (M4) ([a50f6aa](https://github.com/tjorri/paddock/commit/a50f6aa504d62390c0901983eff21284b793ae24))
* **collector:** generic collector sidecar (M6) ([35bd68f](https://github.com/tjorri/paddock/commit/35bd68fff2404d5010fc10d1e12ef7f9fd3a9498))
* **controller:** HarnessRun controller lifecycle (M3) ([164329b](https://github.com/tjorri/paddock/commit/164329b10c1d8776b53da7de2dee0b2610d98013))
* **controller:** native sidecars + output ConfigMap ingestion (M7) ([a315686](https://github.com/tjorri/paddock/commit/a315686d204e71be771565c9c55c8f2de1d6123a))
* **controller:** Workspace controller + seed Job (M2) ([e460529](https://github.com/tjorri/paddock/commit/e460529512a8f9660c4d0c454b0a5eac16fdb6a1))
* **e2e:** echo pipeline end-to-end on Kind (M8) ([4710426](https://github.com/tjorri/paddock/commit/471042656d3a987a45b41a43c5f58f213a3b5182))
* **images:** claude-code harness + adapter (M10) ([ab74ff3](https://github.com/tjorri/paddock/commit/ab74ff3d938a778baa8ece5f989f03d1952cb03d))
* **images:** paddock-echo harness + adapter-echo (M5) ([429a8f9](https://github.com/tjorri/paddock/commit/429a8f9edb2cc1f739de2126463bb307092567b2))


### Bug Fixes

* **claude-code:** surface is_error=true as Job Failed ([2ad9821](https://github.com/tjorri/paddock/commit/2ad9821db00b66d0bb1e4ba713a7800ccba045e6))


### Documentation

* README + CONTRIBUTING + Helm chart + ADR-0010 (M11) ([c56ac4b](https://github.com/tjorri/paddock/commit/c56ac4bdcb56affa6db9b3245ccc774f53602ef6))
* swap paddock-dev → tjorri org references ([c9994b2](https://github.com/tjorri/paddock/commit/c9994b2678ab62589955394b9afdacca0ba1188c))


### Chores

* bootstrap initial release as 0.1.0 ([d88d33f](https://github.com/tjorri/paddock/commit/d88d33f2aecbc48c947c8f13452ffce36fde448c))
