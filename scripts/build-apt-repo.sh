#!/usr/bin/env bash
# Build a signed apt repository from .deb files.
#
#   scripts/build-apt-repo.sh DEB_DIR OUT_DIR
#
# Layout written to OUT_DIR (publish it as the root of GitHub Pages):
#   pool/main/b/bosun/*.deb
#   dists/stable/main/binary-{amd64,arm64}/Packages{,.gz}
#   dists/stable/Release  Release.gpg  InRelease
#   bosun-archive-keyring.gpg   (binary keyring users put in /etc/apt/keyrings)
#   bosun.asc                   (the same key, armored)
#
# Signing reads the private key from $GPG_PRIVATE_KEY (armored) and its
# passphrase, if any, from $GPG_PASSPHRASE. With no key the repository is built
# unsigned, which is only for local testing: set REQUIRE_SIGNED=1 (the release
# workflow does) to make a missing key an error. The script never generates a key.
set -euo pipefail

deb_dir="${1:?usage: build-apt-repo.sh DEB_DIR OUT_DIR}"
out="${2:?usage: build-apt-repo.sh DEB_DIR OUT_DIR}"
suite="${APT_SUITE:-stable}"
archs=(amd64 arm64)

shopt -s nullglob
debs=("$deb_dir"/*.deb)
[ "${#debs[@]}" -gt 0 ] || { echo "no .deb files in $deb_dir" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out/pool/main/b/bosun"
cp "${debs[@]}" "$out/pool/main/b/bosun/"

cd "$out"
for arch in "${archs[@]}"; do
  d="dists/$suite/main/binary-$arch"
  mkdir -p "$d"
  # --arch limits the index to one architecture; scanning from the repo root keeps
  # the pool paths in Packages relative to it.
  dpkg-scanpackages --arch "$arch" pool >"$d/Packages" 2>/dev/null
  gzip -9nk "$d/Packages"
done

# Release lists every index with its hash and size. It is written outside the
# suite dir first, or it would list itself.
release_tmp="$(mktemp)"
apt-ftparchive \
  -o "APT::FTPArchive::Release::Origin=bosun" \
  -o "APT::FTPArchive::Release::Label=bosun" \
  -o "APT::FTPArchive::Release::Suite=$suite" \
  -o "APT::FTPArchive::Release::Codename=$suite" \
  -o "APT::FTPArchive::Release::Architectures=${archs[*]}" \
  -o "APT::FTPArchive::Release::Components=main" \
  release "dists/$suite" >"$release_tmp"
mv "$release_tmp" "dists/$suite/Release"
chmod 644 "dists/$suite/Release"

if [ -z "${GPG_PRIVATE_KEY:-}" ]; then
  if [ "${REQUIRE_SIGNED:-0}" = 1 ]; then
    echo "GPG_PRIVATE_KEY is not set and REQUIRE_SIGNED=1" >&2
    exit 1
  fi
  echo "warning: GPG_PRIVATE_KEY not set; the repository is UNSIGNED (testing only)" >&2
  exit 0
fi

# A throwaway keyring, so nothing touches the runner's or your own GnuPG home.
export GNUPGHOME
GNUPGHOME="$(mktemp -d)"
trap 'gpgconf --kill gpg-agent >/dev/null 2>&1 || true; rm -rf "$GNUPGHOME"' EXIT
chmod 700 "$GNUPGHOME"
printf '%s\n' "$GPG_PRIVATE_KEY" | gpg --batch --quiet --import
fpr="$(gpg --batch --list-secret-keys --with-colons | awk -F: '/^fpr:/ {print $10; exit}')"
[ -n "$fpr" ] || { echo "no secret key was imported" >&2; exit 1; }

sign=(gpg --batch --yes --pinentry-mode loopback --local-user "$fpr")
if [ -n "${GPG_PASSPHRASE:-}" ]; then sign+=(--passphrase "$GPG_PASSPHRASE"); else sign+=(--passphrase ""); fi
"${sign[@]}" --clearsign -o "dists/$suite/InRelease" "dists/$suite/Release"
"${sign[@]}" --armor --detach-sign -o "dists/$suite/Release.gpg" "dists/$suite/Release"
gpg --batch --export "$fpr" >bosun-archive-keyring.gpg
gpg --batch --armor --export "$fpr" >bosun.asc
echo "signed with key $fpr"
