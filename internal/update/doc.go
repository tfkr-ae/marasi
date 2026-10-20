// Package update finds, verifies, and installs updates of the installed
// build (GLOSSARY "Updates"). It has no service dependency; the service
// wraps it with channels, scheduling, events, and the control API.
//
// New takes the build's version, embedded minisign public key, and GitHub
// base URL, and refuses development builds with ErrDevBuild. Check returns
// the update on a channel, ordered by IsUpdate (docs/adr/2026-10-06-date-release-tags.md, docs/adr/2026-10-18-switching-channel-never-downgrades.md); Latest
// returns the latest build even when older, for a reinstall.
//
// Install runs download, verify, install (docs/adr/2026-10-18-updates-are-signed-with-an-embedded-key.md, docs/adr/2026-10-18-service-instances-install-updates.md):
//
//  1. refuse an unwritable install directory before downloading
//  2. fetch checksums.txt and checksums.txt.minisig and verify the signature;
//     the build is never fetched when it fails
//  3. download the build beside the target, then check its SHA-256 and, on
//     macOS, that the installed build's Apple team signed it
//  4. replace the target: rename over it on Unix; on Windows rename the
//     running executable aside, and CleanupAside removes it on next start
//
// AcquireLock holds one install per installed build across instances and
// names its holder.
package update
