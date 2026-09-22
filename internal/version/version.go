// Package version is the single source of truth for barrage's released
// version. Tagged CI builds override it with -ldflags -X from the git tag;
// local/go-run builds report whatever the source declares.
package version

// Version is the current released version, e.g. "v0.6.3". Bump it with
// scripts/release.sh, never by hand across the repo.
var Version = "v0.6.3"
