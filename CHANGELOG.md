# Changelog

## [0.3.0](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.2.0...v0.3.0) (2026-08-30)


### Features

* read and write pbzb payloads with a pure-Go LZBITMAP ([519e895](https://github.com/deploymenttheory/go-macos-pkg/commit/519e8953f927f39381d109c76ddaf5a2fd828dc8))
* read and write pbzb payloads with a pure-Go LZBITMAP ([8b95d9c](https://github.com/deploymenttheory/go-macos-pkg/commit/8b95d9c7fee1659aaffe1c4d910c25dd4d272b2d))
* write pbze payloads, and refuse the containers macOS cannot read ([a65e879](https://github.com/deploymenttheory/go-macos-pkg/commit/a65e8798edb1ad275bd31528b80ee06a7b550c57))
* write pbze payloads, and refuse the containers macOS cannot read ([99bd3ce](https://github.com/deploymenttheory/go-macos-pkg/commit/99bd3ce2a3b37343b71ec438b50dc0acf6c9a98c))

## [0.2.0](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.1.0...v0.2.0) (2026-08-30)


### Features

* carry hard links and extended attributes as pkgbuild does ([dd0d062](https://github.com/deploymenttheory/go-macos-pkg/commit/dd0d062607815f4fa9e7e5d440621272e41206ca))
* carry hard links and extended attributes as pkgbuild does ([284682f](https://github.com/deploymenttheory/go-macos-pkg/commit/284682faa311d13a9aba1b7599e6997b6f74736e))
* read the pbz* payload family and write pbzx payloads ([0154bc2](https://github.com/deploymenttheory/go-macos-pkg/commit/0154bc263cb39c80af66126080c3ff52d7ca1db0))
* read the pbz* payload family and write pbzx payloads ([b86a4a1](https://github.com/deploymenttheory/go-macos-pkg/commit/b86a4a13159c92df5444d11d37d3aaaedc8727f5))
* **verify:** embed Apple's G2, G3 and Platform roots ([b3ed4f6](https://github.com/deploymenttheory/go-macos-pkg/commit/b3ed4f69854a2067b9680b0df0170f3a7fbb113a))
* **verify:** embed Apple's G2, G3 and Platform roots ([33f0c89](https://github.com/deploymenttheory/go-macos-pkg/commit/33f0c897e8ce8fb0427fb5a2e1c2796420a2d304))


### Bug Fixes

* keep extended attributes a host refuses, and let a repack override them ([073f29b](https://github.com/deploymenttheory/go-macos-pkg/commit/073f29b096d48b4bd1dc84c67b0c1dcdc3119ce7))
* write a kept sidecar to the checked path, not the raw archive name ([9486645](https://github.com/deploymenttheory/go-macos-pkg/commit/9486645fa475aabb0d7d3045f5a9f9a9a34920df))

## 0.1.0 (2026-08-29)


### Features

* cross-platform macOS package toolkit (inspect, build, sign, notarize, staple) ([b010b4b](https://github.com/deploymenttheory/go-macos-pkg/commit/b010b4bc969d46c765b16ce42c351c59ba598da0))
* cross-platform macOS package toolkit (inspect, build, sign, notarize, staple) ([876ca9c](https://github.com/deploymenttheory/go-macos-pkg/commit/876ca9c627c171af4ff820d097912850e6b755fc))


### Bug Fixes

* **build:** count a directory's children under its own name ([a5a838f](https://github.com/deploymenttheory/go-macos-pkg/commit/a5a838f2440bdfdc5e2423b867ef031b9ea6d4bb))
* **staple:** close the source before renaming over it, for in-place stapling on Windows ([05f3af0](https://github.com/deploymenttheory/go-macos-pkg/commit/05f3af08e8acf777d7e1a422703686cda54ca9d1))

## Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
