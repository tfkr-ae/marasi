# Updates are verified against a signing key built into Marasi

A service instance installs an update only when its `checksums.txt` carries a valid ed25519 (minisign) signature from the key whose public half is built into the running build, and the asset's SHA-256 matches. On macOS the new binary must also be signed by the same Apple team. One key signs release and nightly, and it is the same key that signs marasi-app; its private half is a GitHub Actions secret used only by publish jobs. Marasi is a security tester's proxy and the Windows and Linux binaries carry no code signature, so without this anyone able to replace a release asset could have every installed build run their code.

**Considered options:** HTTPS from GitHub plus `checksums.txt` alone; a key per channel; a key per repository. Rejected because the checksums sit beside the binaries, so whoever can swap one can swap both, and every channel and repository is signed by the same CI owner, so separate keys add no protection.

**Consequences:** The key is very hard to change. To rotate it, ship a release that embeds the new public key and is signed with the old one; an installed build that skips that release must be reinstalled by hand. Losing the private key means every installed build needs a manual reinstall. Releases published before this ADR are unsigned and are never offered as updates.
