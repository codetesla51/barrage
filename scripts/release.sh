#!/usr/bin/env bash
# Cut a barrage release from a single version argument.
#
# Usage: ./scripts/release.sh v0.6.0
#
# Bumps internal/version (the Go source of truth) plus every doc/script spot
# that prints the version, runs the build/vet/test/gofmt gate, commits as
# `chore(release)`, tags, and pushes. CI then cross-compiles the binaries and
# publishes the GitHub release, so the tag is the whole ceremony. Never bump
# the version by hand across the repo — this is the one knob.
set -euo pipefail

VER="${1:?usage: scripts/release.sh vX.Y.Z (e.g. v0.6.0)}"
case "$VER" in
  v*) ;;
  *) echo "error: version must look like v0.6.0 (got $VER)" >&2; exit 1 ;;
esac

VERSION_FILE="internal/version/version.go"
CUR="$(sed -n 's/.*var Version = "\(v[^"]*\)".*/\1/p' "$VERSION_FILE")"
if [ -z "$CUR" ]; then
  echo "error: could not read the current version from $VERSION_FILE" >&2
  exit 1
fi
if [ "$CUR" = "$VER" ]; then
  echo "already on $VER — nothing to do"
  exit 0
fi

# Bedrock: only these files show the version, always as one bare token, so a
# blind replace is safe. Add a file here when it starts printing the version.
read -r -d '' TARGETS <<'EOF' || true
internal/version/version.go
README.md
SKILL.md
install.sh
EOF

echo "bumping $CUR -> $VER ..."
while read -r f; do
  [ -f "$f" ] && sed -i "s/${CUR}/${VER}/g" "$f"
done <<< "$TARGETS"
grep -rln "$VER" internal README.md SKILL.md install.sh || true

echo "running gate..."
go build ./...
go vet ./...
go test ./...
if gofmt -l . | grep -q .; then
  echo "error: gofmt reports unformatted files:" >&2
  gofmt -l . >&2
  exit 1
fi

git add -A
git commit -m "chore(release): bump version to $VER"
git tag "$VER"
git push origin main
git push origin "$VER"
echo "released $VER — CI is building binaries and publishing the release"