# Contribution

Thanks for considering contributing to this project! We are really glad you are reading this, because we need volunteer developers to help this project come to fruition.

Please note we have a code of conduct, please follow it in all your interactions with the project.

## Issues

If you find any bugs, please file an issue in the [GitHub issues][GitHubIssues] page. Please fill out the provided template with the appropriate information.

If you are taking the time to mention a problem, even a seemingly minor one, it is greatly appreciated, and a totally valid contribution to this project. Thank you!

## Pull requests

Pull request titles follow [Conventional Commits](https://www.conventionalcommits.org/)
(`feat:`, `fix:`, `docs:`, `ci:`, ...); release-please turns them into the
changelog and the next version, so the prefix matters.

## Rules the code keeps

- The binary never calls an Apple tool. `pkgbuild`, `productbuild`,
  `productsign`, `pkgutil`, `xar`, `lsbom`, `stapler`, `notarytool` and the
  keychain appear only in `acceptance/` on macOS, as independent oracles.
  CI fails a pull request that imports `os/exec` anywhere else.
- Every command must work end to end on Linux and Windows. A feature that
  only works on macOS is a bug.
- The xar, bom and cpio code is our own. Reference implementations are read
  and credited in `NOTICE`; they are not imported.

## Proving a refactor changed nothing

Renames, restructures and "no functional change" cleanups in the write path are
exactly where a silent regression hides. Since the writers are deterministic
(see [docs/reproducible-output.md](docs/reproducible-output.md)), the check is a
hash comparison:

```sh
# On the base commit
go build -o /tmp/macospkg-before ./cmd/macospkg
/tmp/macospkg-before build ./some/root /tmp/before.pkg --identifier com.example.x --version 1.0 --source-date-epoch 1700000000

# On your branch
go build -o /tmp/macospkg-after ./cmd/macospkg
/tmp/macospkg-after build ./some/root /tmp/after.pkg --identifier com.example.x --version 1.0 --source-date-epoch 1700000000

shasum -a 256 /tmp/before.pkg /tmp/after.pkg   # the two hashes must match
```

If the hashes legitimately differ because your change *is* meant to move bytes,
say so explicitly and describe which field moved and why.

## Regenerating the fixtures

`testdata/cli` holds packages produced by Apple's own tools. They are the
oracle the reader is tested against on every platform, so they are committed
rather than built during the test run. Regenerating them is a deliberate,
macOS-only maintenance step:

```sh
scripts/gen-fixtures.sh
```

<!-- References -->

[GitHubIssues]: <https://github.com/deploymenttheory/go-macos-pkg/issues>
