# Changelog

## [0.4.1](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.4.0...v0.4.1) (2026-08-31)


### Bug Fixes

* correct Size64 keying and large payload segmentation ([5d43ac3](https://github.com/deploymenttheory/go-macos-pkg/commit/5d43ac3f540fa8350b2dbca7c1103b0f37281366))
* correct Size64 keying and large payload segmentation ([10bc850](https://github.com/deploymenttheory/go-macos-pkg/commit/10bc85083ac81f4c9b1317546fc8ed50456109cc))

## [0.4.0](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.3.2...v0.4.0) (2026-08-31)


### Features

* 1:1 result parity with the macOS built-in tools ([b46c320](https://github.com/deploymenttheory/go-macos-pkg/commit/b46c320367d4d0d803dc6def1aa082143afcc8de))
* accept the credential variable names electron-builder uses ([dedb846](https://github.com/deploymenttheory/go-macos-pkg/commit/dedb8466cd4be47e7bd50a23c38930aefa4447d9))
* add --analyze and --component-plist for per-bundle rules ([14ac113](https://github.com/deploymenttheory/go-macos-pkg/commit/14ac11355b0ee028af1c5aacfae6bc4ac875b5fb))
* add --component and --prior, pkgbuild's other two ways to build ([d88190d](https://github.com/deploymenttheory/go-macos-pkg/commit/d88190de0cd3b6d4292fc83290a13db76a77df0d))
* add flatten, the inverse of expand ([1918e72](https://github.com/deploymenttheory/go-macos-pkg/commit/1918e72f9d1d5980f4ce8827b40b7ebbdbecac7b))
* add product --product, the pre-install requirements property list ([b12fc11](https://github.com/deploymenttheory/go-macos-pkg/commit/b12fc1193ed7d7df4a0d94d21d6971c34c4ded1d))
* add product --scripts, --plugins and --ui ([ac75675](https://github.com/deploymenttheory/go-macos-pkg/commit/ac75675490009cbf7561748b07a3f583ca6f0aee))
* add product --synthesize and --package-path, and rewrite a supplied Distribution ([cfd8cd3](https://github.com/deploymenttheory/go-macos-pkg/commit/cfd8cd39bce40b719a70e034dd0dc5d409cd3646))
* apply pkgbuild's default payload filters, and add --filter ([ee92e8c](https://github.com/deploymenttheory/go-macos-pkg/commit/ee92e8ca902aa52c1070ed4b807887ff8fb4cfe5))
* build the component in place with product --root, --content and --component ([083580e](https://github.com/deploymenttheory/go-macos-pkg/commit/083580e1976d204b91f792fc1ca04b1351952341))
* check whether the signing certificate has been revoked ([7e725ca](https://github.com/deploymenttheory/go-macos-pkg/commit/7e725ca49842b91322ed606902f4f52548cb4148))
* narrow a listing with --only-files, --only-dirs and --regexp ([559eca5](https://github.com/deploymenttheory/go-macos-pkg/commit/559eca5fcfef9ec69622c16061360554bf01da19))
* read a volume's receipt database ([fd3e4f4](https://github.com/deploymenttheory/go-macos-pkg/commit/fd3e4f477c63820964db9328907b20f838954216))
* remember credentials under a name, and write property lists ([1cb1b47](https://github.com/deploymenttheory/go-macos-pkg/commit/1cb1b4709288fe9a363e73719e7589b9309a6c14))
* require macOS 12 where a product carries a large payload ([c07bb76](https://github.com/deploymenttheory/go-macos-pkg/commit/c07bb76b5c5d63d2250044d27182d6d5e3a05c04))
* store a payload uncompressed, with --compression none ([71bff18](https://github.com/deploymenttheory/go-macos-pkg/commit/71bff1807e297634c0e710c8c49e077abe0e6569))
* submit disk images and archives, upload in parts, and ask for a webhook ([a06db54](https://github.com/deploymenttheory/go-macos-pkg/commit/a06db54f370d6c1918d3c627f17fe9ebbea3b491))
* write --large-payload packages, not only read them ([1fcc1e7](https://github.com/deploymenttheory/go-macos-pkg/commit/1fcc1e7d6c495db624b33b459d31b223837ea795))


### Bug Fixes

* accelerate uploads by default, as Apple's own example does ([e3544ae](https://github.com/deploymenttheory/go-macos-pkg/commit/e3544ae3ec8d533e0e7e4c4cd38b15140433228c))
* follow pkgbuild's rules for framework paths and nested bundles ([6084264](https://github.com/deploymenttheory/go-macos-pkg/commit/6084264f06f47da1d2bf40bd7ac7699b51d1b47e))
* make flatten and the receipts tests work off macOS ([785c92d](https://github.com/deploymenttheory/go-macos-pkg/commit/785c92dac2bc502f90c63e16f8484d59e50a4318))
* only declare standalone on a Distribution productbuild re-serialises ([324c39e](https://github.com/deploymenttheory/go-macos-pkg/commit/324c39e9b515d821708528e0558f5fcc697d7baf))
* write PackageInfo and Distribution exactly as Apple's tools write them ([45e85e3](https://github.com/deploymenttheory/go-macos-pkg/commit/45e85e35766c60f1f3a7c7a6c76e13c37634fe8a))

## [0.3.2](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.3.1...v0.3.2) (2026-08-30)


### Bug Fixes

* bump Go to 1.26.6 and keep the timestamp fixture out of EOL conversion ([f68caf7](https://github.com/deploymenttheory/go-macos-pkg/commit/f68caf732de27f44a71ad64e609270a1da0b8cbd))
* **security:** verify timestamp tokens, and stop extraction following links ([d97a6e8](https://github.com/deploymenttheory/go-macos-pkg/commit/d97a6e86a983bd63648691501e312e16ea4e26e2))
* **security:** verify timestamp tokens, and stop extraction following links ([55cac30](https://github.com/deploymenttheory/go-macos-pkg/commit/55cac302914429c7c9b6c153b745cd5ab9366db9))

## [0.3.1](https://github.com/deploymenttheory/go-macos-pkg/compare/v0.3.0...v0.3.1) (2026-08-30)


### Bug Fixes

* match sentinel errors by identity, not by message text ([85ed68b](https://github.com/deploymenttheory/go-macos-pkg/commit/85ed68be62c144efc1310972cc4c70c793d79058))
* match sentinel errors by identity, not by message text ([7c32ea8](https://github.com/deploymenttheory/go-macos-pkg/commit/7c32ea85968b8d62b252cb09b6da0150c8a643bf))


### Performance Improvements

* stop the LZBITMAP search sweeping history that never matches ([684bb1d](https://github.com/deploymenttheory/go-macos-pkg/commit/684bb1df4f9625f2e22a93101571da44097521a4))
* stop the LZBITMAP search sweeping history that never matches ([bc218b7](https://github.com/deploymenttheory/go-macos-pkg/commit/bc218b78172e2ec0ab12bf6823892f16f9dfaea3))

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
