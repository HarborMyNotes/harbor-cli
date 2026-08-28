#!/usr/bin/env python3
#
# Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
# Date: 2026-08-27
#
"""Point the Homebrew tap formula at a new harbor-cli release.

The formula in HarborMyNotes/homebrew-harbor pins an explicit release tag and a
SHA-256 per platform. Homebrew scans the version it reports out of that url, so
the url and the checksum have to move together or an install would verify one
build while claiming to be another. Rewriting both here, from the checksums.txt
the release just published, is what keeps them in step.

Exits non-zero if any platform the formula covers was not updated, so a formula
that drifts out of the shape this expects fails the release instead of silently
publishing a stale pin.
"""

import argparse
import pathlib
import re
import sys

# The release assets the formula installs. checksums.txt also lists the Windows
# binary, which Homebrew has no use for.
ASSETS = (
    "harbor-darwin-arm64",
    "harbor-darwin-amd64",
    "harbor-linux-arm64",
    "harbor-linux-amd64",
)


# Parses a `sha256sum` manifest into {asset name: checksum}.
def read_checksums(path):
    sums = {}
    for line in pathlib.Path(path).read_text().splitlines():
        if not line.strip():
            continue
        digest, name = line.split()
        sums[name] = digest
    return sums


# Repoints every release url at the new tag. The version segment is the only
# part that moves; asset names are a public contract set by the release build.
def set_version(text, version):
    return re.sub(
        r"(/releases/download/)v\d+\.\d+\.\d+(/harbor-)",
        rf"\g<1>{version}\g<2>",
        text,
    )


# Replaces the sha256 that follows an asset's url. Matching on the url rather
# than on position is what stops a reordered formula from getting the wrong
# platform's checksum.
def set_checksum(text, asset, digest):
    pattern = rf'(url "[^"]*/{re.escape(asset)}"\s*\n\s*sha256 ")[0-9a-f]{{64}}(")'
    return re.subn(pattern, rf"\g<1>{digest}\g<2>", text)


# Rewrites the formula in place and reports whether anything actually changed.
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--formula", required=True, help="path to harbor.rb")
    parser.add_argument("--checksums", required=True, help="path to checksums.txt")
    parser.add_argument("--version", required=True, help="release tag, e.g. v0.1.41")
    args = parser.parse_args()

    if not re.fullmatch(r"v\d+\.\d+\.\d+", args.version):
        sys.exit(f"version must look like v1.2.3, got {args.version!r}")

    sums = read_checksums(args.checksums)
    formula = pathlib.Path(args.formula)
    original = formula.read_text()

    updated = set_version(original, args.version)
    for asset in ASSETS:
        if asset not in sums:
            sys.exit(f"{asset} is missing from the checksums manifest")
        updated, count = set_checksum(updated, asset, sums[asset])
        if count != 1:
            sys.exit(f"expected exactly one url+sha256 pair for {asset}, matched {count}")

    if updated == original:
        print(f"Formula already at {args.version}; nothing to do.")
        return 0

    formula.write_text(updated)
    print(f"Formula bumped to {args.version}.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
