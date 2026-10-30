# Packaging (D-4)

GoReleaser drives releases. Nothing here has been pushed, tagged or signed: the owner steps are in `RELEASE.md`.

## What exists

| File | Purpose |
|---|---|
| `.goreleaser.yaml` | builds (linux and darwin x amd64 and arm64, `CGO_ENABLED=0`, `-trimpath`, `-X main.version`), tarballs, `.deb` and `.rpm` via nfpm, `checksums.txt` (sha256), and a Homebrew formula for `jmvaldez/homebrew-tap` |
| `packaging/postinstall.sh` | the `.deb`/`.rpm` post-install script: prints the plugin, status line and tmux steps; runs none of them |
| `scripts/build-apt-repo.sh` | builds an apt repository (`pool/`, `dists/stable/main/binary-{amd64,arm64}/Packages`, `Release`, `Release.gpg`, `InRelease`) from the `.deb` files; signs with `$GPG_PRIVATE_KEY`; never generates a key |
| `.github/workflows/ci.yml` | `vet`, `test -short`, `cross`, `staticcheck`; a job with a private tmux running the opt-in suites; a snapshot-release job |
| `.github/workflows/release.yml` | on `v*` tags: checks the tag equals `VERSION`, runs GoReleaser, then builds and signs the apt repository and publishes it to `gh-pages` |
| `Makefile` | `release` (snapshot into the gitignored `dist/`), `staticcheck`, `test-short`; tool versions are pinned (`GORELEASER_VERSION`, `STATICCHECK_VERSION`) and run through `go run`, so nothing is installed system-wide |
| `main.go` | version stamping: `-X main.version=...` in release builds; otherwise `<VERSION>-dev+<vcs.revision>` or `<VERSION>-dev` |

What a package contains (`dpkg-deb -c`):

```
/usr/bin/bosun                                                  0755
/usr/share/bosun/.claude-plugin/marketplace.json                0644
/usr/share/bosun/plugin/.claude-plugin/plugin.json              0644
/usr/share/bosun/plugin/hooks/hooks.json                        0644
```

`claude plugin marketplace add /usr/share/bosun` works on a package install: the marketplace manifest sits beside the `plugin/` dir it points at. Homebrew installs the same two items into the formula's `pkgshare`; the formula's caveats print the matching commands.

## Verified on this machine

All with `make release` (snapshot, version `0.1.0-SNAPSHOT-none`; the repository has no commits, so GoReleaser reports the commit as `none`):

- Artifacts in `dist/`: four tarballs (`linux`/`darwin` x `amd64`/`arm64`), a `.deb` and an `.rpm` for each of `amd64` and `arm64`, `checksums.txt`, `homebrew/bosun.rb`, `artifacts.json`.
- `dpkg-deb -I` shows `Package: bosun`, `Architecture: amd64`, `Recommends: tmux`, a `postinst`; `dpkg-deb -c` shows the contents above with 0755 directories and 0644 files. (An earlier draft used an nfpm `tree` entry and produced 0644 directories, which non-root users cannot enter; `TestGoreleaserShipsEveryPluginFile` now guards the file-by-file list.)
- The linux/amd64 tarball extracts to `bosun` (statically linked ELF) and `share/bosun/plugin/...`; `./bosun --version` prints `bosun 0.1.0-SNAPSHOT-none`.
- In an `ubuntu:22.04` container (docker 29.7): `apt-get install ./bosun_..._amd64.deb` installs, prints the post-install message, `bosun --version` works, `bosun doctor --json` runs (exit 1 on a bare container, as it should: no plugin, no heartbeat), and `dpkg -r bosun` removes the binary. The same container then installed `bosun` from the apt repository this script built (an unsigned local repository served over `file://` with `[trusted=yes]`; apt read `Release`, `Packages` and the pool correctly).
- `actionlint` (container `rhysd/actionlint`) reports nothing for both workflows; all three YAML files parse.
- `make staticcheck` (v0.8.2) is clean after removing five unused declarations.

Not verified here, by design: the signing step of `build-apt-repo.sh` (it needs your key and the instructions forbid generating one; the script is `shellcheck`-clean and its unsigned path is exercised), `rpm` contents (no `rpm` tool or image was used; GoReleaser's nfpm builds the `.rpm` from the same contents list), and anything that needs GitHub (release upload, the tap push, Pages).

## Notes and judgment calls

1. **`brews` is deprecated** in GoReleaser 2.x (it prefers `homebrew_casks`, which are macOS-only). D-4 asks for a formula that works on linuxbrew and macOS, so `brews` stays; `goreleaser check` exits 2 on the deprecation notice, so CI runs `make release` instead of `check`.
2. **Pinned tools.** `goreleaser v2.19.0` needs Go 1.27.3 and staticcheck `v0.8.2` needs 1.26; `GOTOOLCHAIN=auto` fetches them into the module cache on first use.
3. **Version rules.** A release build reports exactly the tag's version. The plugin manifests carry `VERSION`, and the release workflow refuses a tag that differs from it. `doctor` compares plugin and binary by major.minor, so `0.1.0-dev+abc1234` and `0.1.0` agree.
4. **No license is declared** in the nfpm or Homebrew metadata, because the repo has none yet. `RELEASE.md` lists adding a `LICENSE` and the `license:` keys as an owner step. The package maintainer is `jmvaldez <jmvaldez@users.noreply.github.com>`.
5. **The apt repository holds the latest release only.** The workflow keeps old files on `gh-pages` but rewrites `dists/` from the current release's `.deb` files.
6. **Architectures.** The `.deb` and `.rpm` are built for amd64 and arm64. Darwin gets tarballs and the formula only.

## User install snippets (for the README once the repo is public)

Debian and Ubuntu (apt repository):

```sh
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://jmvaldez.github.io/bosun/bosun.asc | sudo tee /etc/apt/keyrings/bosun.asc >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/bosun.asc] https://jmvaldez.github.io/bosun stable main" |
  sudo tee /etc/apt/sources.list.d/bosun.list
sudo apt update && sudo apt install bosun
```

A single `.deb` from the Releases page: `sudo apt install ./bosun_<version>_amd64.deb`.

Homebrew (macOS and Linux):

```sh
brew install jmvaldez/tap/bosun
```

Tarball: extract, put `bosun` on your PATH, and use `share/bosun` as the marketplace path.

Then, for every method (the post-install message prints the same):

```sh
claude plugin marketplace add /usr/share/bosun   # or $(brew --prefix)/share/bosun, or your extracted share/bosun
claude plugin install bosun@bosun
bosun install      # prints the status line and tmux edits for your setup
bosun doctor
```
