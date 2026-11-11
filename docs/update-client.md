# Desktop update operation

The first 0.2.0 installation is manual. The delivered 0.1 clients have no update
engine. Version 0.2.0 embeds the public Ed25519 trust key and build sequence 1;
Version 0.3.1 keeps that trust key and advances to build sequence 3 (0.3.0 used sequence 2). The separate
Win7 compatibility client currently uses full-package manual updates. For standard clients,
future releases must increment **both** application SemVer precedence and
`BUILD_SEQUENCE` in `client/src-tauri/src/update/manifest.rs`. A version's build
metadata (`+...`) alone is not a newer version. Keep Cargo, Tauri and npm versions
consistent. Signing format and offline publication commands are in
[update-format.md](update-format.md).

For an independent deployment, configure your own server origin in
`client/src-tauri/src/deployment.rs` and your own release public key before the
first client build. The repository's public key is not a signing credential.
Existing installations require releases signed by the private key corresponding
to their embedded public key; replacing the source public key does not rotate
the trust held by already installed clients.

Windows 0.3.2 / build sequence 4 fixes the restart helper launch. Older standard Windows clients can fail before launching their update helper because its executable is still open for writing. These installations need one manual installation of 0.3.2: finish transfers, quit from the tray, then run the current-user installer over the existing installation. Keep the existing application data; do not uninstall or delete transfer state. This maintenance release targets standard Windows; macOS and Win7 installers remain at 0.3.1.

The desktop begins background checks after local startup, then every six hours.
Checks and object GETs use only the compiled server origin, no authentication or
cookies, no proxy discovery and no redirects. Downloads use one request at a time
with a 4 MiB/s ceiling. HTTP 206 or Content-Range is rejected: each content-addressed
object is fetched whole, so there is no ambiguous byte-range reconstruction.
Verified object files persist across restart. A ready release persists and is
reverified locally on restart, including expiry. A signed sequence already
accepted cannot be replaced by a lower sequence or different signed manifest at
the same sequence; unsigned envelope whitespace does not change its identity.

Every COPY reads the installed bytes, checks its length and SHA256, and only then
credits reuse. Missing or changed blocks fetch the signed content-addressed
object. A failed delta requests the signed full file SHA256/size from that same
manifest, then verifies the full result. Signature or manifest validation failure
never enables a fallback. All reconstructed files, modes and the complete tree
are verified before the client displays ready. The status reports actual payload
bytes downloaded, including failed attempts; retries can exceed total target size.

The user chooses restart to install. The shared native ActivityGate atomically
refuses installation while uploads, downloads, peer sends, exports or attachment
preparation are active. A busy result becomes deferred and asks the user to retry
when work finishes; it never pauses work or installs automatically. A successful
installation permit blocks new work until process exit. The existing services
are shut down only after the independent trusted helper confirms readiness.

Installation locations:

- macOS: a complete `LinkNestX.app` under the current user's home, with that user
  owning the bundle and its writable parent. `~/Applications/LinkNestX.app` is the
  recommended location. `/Applications`, disk images and shared/read-only roots
  require a manual installation. The entry is `Contents/MacOS/linknestx`.
- Windows: the current-user `%LOCALAPPDATA%` installation whose final directory is
  `LinkNestX`. The update manifest contains exactly `LinkNestX.exe`; it exchanges
  that executable only, leaving the NSIS uninstaller and other installation
  infrastructure in place. Changes requiring runtime sidecars or install layout
  changes require a manual full installer. `tauri.windows.conf.json` sets the
  packaged executable's case/name to match this format.

The helper is a byte-for-byte copy of the current trusted executable and starts
in an early native-only mode. IPC accepts no helper path, target path, arbitrary
URL or shell command. An installation job has a random identifier; the helper
rechecks its copy hash, signed manifest, platform, target confinement, complete
payload and old backup digest. Filesystem links/reparse points and special files
are rejected. macOS additionally runs the system `codesign --verify --deep
--strict` on the reconstructed bundle. Bundles containing framework symlinks are
currently unsupported and receive manual-install guidance. Ad-hoc signatures
provide code integrity for local builds but are not Developer ID/notarization.

After the old process exits, macOS atomically exchanges the full app and staged
bundle using `RENAME_SWAP`; Windows uses `ReplaceFileW` to replace the executable
and retain a backup. The target path remains present during the normal swap.
A persisted journal records preparation, swapping, awaiting health, healthy or
rolled back. The new process acknowledges its release version, sequence and
per-install random nonce only after local database/core initialization. Server
reachability is deliberately irrelevant. A wrong nonce, exit or 60-second timeout
kills/drains the attempted child, restores the verified original, and starts it
once. Reverse restoration also uses atomic exchange/replacement while a target
exists; a repeated recovery first recognizes the verified original already in
place, including interruption before the journal was finalized. Compatibility
recovery can restore an already missing target from an earlier incomplete job.
There is no automatic reinstallation loop. The rollback version and reason remain
visible through startup cache restoration and background checks of that same
release, missing manifests, HTTP errors or invalid signatures. An explicit user
check/retry acknowledges that outcome and revalidates the prepared files; a higher
valid signed release may also supersede it. A separate acknowledgment marker
preserves the original rollback phase, error and job history. On a later ordinary startup,
a pending interrupted job invokes the retained trusted helper to recover after
that startup process exits. Backups are retained after health confirmation for
manual recovery; app-private object caches and prior job folders also currently
remain until manually cleaned. No arbitrary recursive cleanup is exposed to JS.

The release signing private key is never bundled; only
`client/update-public-key.json` is embedded. The ignored production private PEM
is separate from test keys and must be backed up securely before publication.
Publication, Windows execution/Defender and installer integration, real macOS
Developer ID/notarization/quarantine, and cross-device production validation are
separate release gates. The local tests do not claim these gates passed.
