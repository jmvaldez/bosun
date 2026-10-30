#!/bin/sh
# Runs after the .deb or .rpm is installed. It only PRINTS: it never edits your
# Claude settings, your tmux config, or anything outside the package.
cat <<'MSG'

bosun is installed (/usr/bin/bosun). To finish setting it up, as your own user:

  bosun setup

MSG
exit 0
