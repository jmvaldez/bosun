# Release: steps for the owner

Nothing below has been run. bosun's build does not create repositories, push, tag, or generate keys; these are the commands for you, in order. `gh` is the GitHub CLI, logged in as `jmvaldez`.

## 0. Before the first release

1. License: MIT (`LICENSE`, set in `.goreleaser.yaml`). Done.
2. Make the first commit (the build never commits):
   ```sh
   cd /mnt/shared/dev/bosun
   git add -A && git status        # check kit/ and build-log/ are what you want public
   git commit -m "bosun 0.1.0"
   ```
   (`kit/` is the spec bundle; add it to `.gitignore` or leave it out of the commit if you do not want it public.)

## 1. The repository

```sh
gh repo create jmvaldez/bosun --public --source . --remote origin
git push -u origin main
```

CI (`.github/workflows/ci.yml`) runs on that push.

## 2. The apt signing key

Generate a key you control (RSA 4096 is the most widely accepted by older apt versions), with or without a passphrase:

```sh
gpg --quick-generate-key "bosun apt repository <jmvaldez@users.noreply.github.com>" rsa4096 sign 5y
gpg --list-secret-keys --keyid-format long      # note the key id (the 40-hex fingerprint)
KEY=<fingerprint>
gpg --armor --export-secret-keys "$KEY" | gh secret set GPG_PRIVATE_KEY -R jmvaldez/bosun
printf '%s' '<passphrase, or leave this line out for none>' | gh secret set GPG_PASSPHRASE -R jmvaldez/bosun
gpg --gen-revoke "$KEY" > ~/bosun-apt-key.revoke.asc    # keep this somewhere safe, off this machine
```

The release workflow imports the secret into a throwaway keyring, signs `Release` (as `InRelease` and `Release.gpg`) and publishes the public key as `bosun.asc` and `bosun-archive-keyring.gpg` beside it. Anyone installing trusts exactly that key.

## 3. The Homebrew tap

```sh
gh repo create jmvaldez/homebrew-tap --public --description "Homebrew formulae"
# A fine-grained personal access token limited to jmvaldez/homebrew-tap with
# Contents: read and write. Create it at https://github.com/settings/personal-access-tokens/new, then:
gh secret set HOMEBREW_TAP_TOKEN -R jmvaldez/bosun      # paste the token
```

GoReleaser pushes `Formula/bosun.rb` to the tap on each tagged release.

## 4. GitHub Pages for the apt repository

The workflow publishes to the `gh-pages` branch, which does not exist until the first release run creates it. After that run:

```sh
gh api -X POST repos/jmvaldez/bosun/pages -f 'source[branch]=gh-pages' -f 'source[path]=/'
```

(or Settings, Pages, Source: Deploy from a branch, `gh-pages`, `/`). The repository then answers at `https://jmvaldez.github.io/bosun`.

## 5. Tag

`VERSION` and the plugin manifests already say `0.1.0`; the release workflow refuses a tag that does not match.

```sh
git tag -a v0.1.0 -m "bosun 0.1.0"
git push origin v0.1.0
gh run watch        # the "release" workflow: goreleaser, then the apt-repo job
```

## 6. Check it

```sh
gh release view v0.1.0                       # tarballs, .deb, .rpm, checksums.txt
brew install jmvaldez/tap/bosun && bosun --version
# apt: the snippet in build-log/PACKAGING.md, then
sudo apt update && apt policy bosun && sudo apt install bosun && bosun --version
curl -fsSL https://jmvaldez.github.io/bosun/dists/stable/InRelease | head -3   # a PGP signed message
```

## Later releases

Bump `VERSION` and the three manifests together (`plugin/.claude-plugin/plugin.json`, `plugin/hooks/hooks.json` args, `.claude-plugin/marketplace.json`; the tests fail until they agree), commit, tag `v<VERSION>`, push the tag.

## If a step fails

- `goreleaser` fails on the tap push: the token lacks write access to `homebrew-tap`; the GitHub release itself is already created, so fix the secret and re-run the failed job.
- The `apt-repo` job fails with "GPG_PRIVATE_KEY is not set": step 2 was skipped. The job refuses to publish an unsigned repository.
- Pages 404: step 4 has not been done, or `gh-pages` has no `index.html`; the apt paths work without one.
