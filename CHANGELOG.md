# Changelog

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
