# Releasing

How to publish a Geko release. [Back to the README](../README.md).

Pushing a `v*` tag runs [`release.yml`](../.github/workflows/release.yml):

1. The `release` job runs the tests, then GoReleaser publishes checksummed
   archives for Linux, macOS and Windows on amd64 and arm64 to GitHub Releases.
2. The `image` job builds the Dockerfile for `linux/amd64` and `linux/arm64`
   with the tag as the version and pushes it to `ghcr.io/yunusemrealps/geko`,
   tagged with the version (`0.1.0`), its minor series (`0.1`) and `latest`.

## Steps

1. Check that CI passes on `master`, including the release snapshot job.
2. Move the entries under **Unreleased** in [CHANGELOG.md](../CHANGELOG.md) to a
   section for the version and date, and commit.
3. Tag and push. The tag is public as soon as it is pushed:

   ```sh
   git tag -a v0.1.0 -m "Geko v0.1.0"
   git push origin v0.1.0
   ```

4. Verify the release:

   ```sh
   gh release view v0.1.0
   gh release download v0.1.0 --pattern 'checksums.txt' --pattern 'geko_*_linux_amd64.tar.gz'
   sha256sum --check --ignore-missing checksums.txt
   docker run --rm ghcr.io/yunusemrealps/geko:0.1.0 version
   ```

5. The first push creates the `geko` package on GitHub as private. Open the
   package settings on GitHub and set its visibility to public, so
   `docker compose pull` works without signing in to ghcr.io.

A failed `image` job leaves the archives published. Fix the cause and re-run
the job from the Actions tab; it pushes the same tags again.
