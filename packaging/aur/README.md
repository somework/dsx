# AUR packaging for dsx

`PKGBUILD` and `.SRCINFO` for the [`dsx-bin`](https://aur.archlinux.org/packages/dsx-bin)
Arch User Repository package. It installs the pre-compiled binary from a GitHub release
(there is no `dsx` source package; the source lives in this repo and builds with `go install`).

This directory is the canonical source. Publishing to the AUR is manual, by a maintainer with
an AUR account and an SSH key registered there — the same split as the Homebrew tap.

## Update on a new release

1. Bump `pkgver` in `PKGBUILD` to the new version (and reset `pkgrel=1`).
2. Refresh both checksums from the release's `checksums.txt`:

   ```sh
   gh release download vX.Y.Z --repo somework/dsx --pattern checksums.txt --output -
   ```

   Copy the `linux_amd64` sum into `sha256sums_x86_64` and `linux_arm64` into
   `sha256sums_aarch64`.
3. Regenerate `.SRCINFO` (on an Arch box) or edit it by hand to match:

   ```sh
   makepkg --printsrcinfo > .SRCINFO
   ```

## Publish to the AUR

```sh
git clone ssh://aur@aur.archlinux.org/dsx-bin.git
cp PKGBUILD .SRCINFO dsx-bin/
cd dsx-bin
git commit -am "upgpkg: dsx-bin X.Y.Z"
git push
```

Test locally first with `makepkg -si` in a clean checkout.
