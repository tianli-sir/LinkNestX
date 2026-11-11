# LinkNestX signed update format v1

The offline Go producer is `server/cmd/release`; it does not publish over the network. `server/internal/releases` is the common validator/static HTTP provider. The Rust consumer must agree on every field below. Signing key is generated locally in ignored `.cache/release-signing/private.pem` (0600); public trust resource is `client/update-public-key.json`. Test fixtures generate separate keys. Never bundle the private PEM or use test keys for production.

The server origin is pinned at build time in `client/src-tauri/src/deployment.rs`; set it to your own deployment, for example `https://files.example.com`, before building clients. Client reads `/updates/{platform}/{arch}/latest.json`. Platform is `windows` or `macos`; architecture `x86_64` or `aarch64` (the Windows format currently supports x64 only; this is not a Windows runtime-verification claim). No cookies/auth header sent to public update routes; no redirects or arbitrary target origins accepted.

Envelope JSON is `{keyId,manifest,signature}`. All three strings: keyId is first16 lowercase SHA256 hex of raw32byte Ed25519 public key; manifest is **unpadded base64url of exact UTF8 JSON bytes**, signature is unpadded base64url of64byte Ed25519 signature. Signed message is literal UTF8/ASCII `LinkNestX-update-manifest-v1` followed by a single zero byte followed by exact manifest bytes. Verify before parsing. Public resource `{keyId,publicKey}` uses same unpadded base64url raw32byte public key. Reject unknown keys, invalid signature, duplicate or unknown fields and trailing data. Never reserialize JSON as the signing input.

Manifest fields:

```json
{
  "schemaVersion":1,
  "appId":"com.linknestx.client",
  "channel":"stable",
  "platform":"windows",
  "arch":"x86_64",
  "version":"0.2.1",
  "buildSequence":2,
  "issuedAt":"2026-11-10T12:00:00Z",
  "expiresAt":"2027-02-08T12:00:00Z",
  "entryPoint":"LinkNestX.exe",
  "totalBytes":262158,
  "files":[{
    "path":"LinkNestX.exe","mode":493,"size":262158,"sha256":"<full file sha256>",
    "blocks":[
      {"sha256":"<block sha256>","size":262144,"basePath":"LinkNestX.exe","baseOffset":0},
      {"sha256":"<block sha256>","size":14}
    ]
  }]
}
```

`mode` is decimal420(0644) or493(0755). Blocks ordered consecutively cover each file exactly, normal block length262144 (last may be shorter); empty file has blocks[] and SHA256(empty). `basePath` and `baseOffset` are optional hints for a matching block in the current installed application. Before reusing bytes verify their expected size+SHA, never trust the hint. If no reusable base or hash mismatch download the block from `/updates/objects/{block.sha256}`. Each full file also exists at `/updates/objects/{file.sha256}`; corrupted/missing delta can fall back to that same signed full file. Limit requests, stream bodies with exact length bounds, write temporary owned files only. Final full file SHA is mandatory for every reconstruction and full fallback. Reuse means actual verified bytes copied, not estimated saving.

macOS root is the inside of complete `.app`; entryPoint is usually `Contents/MacOS/linknestx`. The current Windows installer consumer requires exactly one payload file, `LinkNestX.exe`, and replaces only that executable; sidecars and installation-layout changes require a manual installer. File tree publication refuses symlinks/special files; unsupported bundle frameworks must result in clear manual-update guidance. All paths must be canonical relative forward-slash paths, no empty/dot/dotdot components, colon, backslash, NUL, absolute roots or trailing space/dot. Case-insensitive duplicates and file/directory collisions forbidden; installer rechecks safe containment and rejects path replacement/symlink attacks. Strict consumer can impose additional OS-specific reserved-name rules.

Bounds: manifest encoded<=8MiB, <=4096 files, <=16384 blocks, total and each file<=2147483648bytes; positive sequence, valid semver core/prerelease, issuedAt at most5minutes future, expiry strictly future/after issue and validity<=366days. Consumer additionally matches product/platform/arch, verifies version newer than its installed0.2.0 and buildSequence higher than current/persisted accepted sequence. Persist ready sequence without bricking retry/install of that same already accepted release; no signed network rollback.

Before the **first client build for a new independent deployment**, generate a new signing key and its public resource into ignored local storage:

```sh
go run ./server/cmd/release keygen --private .cache/release-signing/private.pem --public .cache/release-signing/update-public-key.json
```

Back up the private key securely, then intentionally replace `client/update-public-key.json` with that generated public resource before compiling your own clients. Do not replace the key for an existing installation fleet: its clients still trust their embedded key. The repository includes only a public key, not a private key that another deployment can use to sign releases. `keygen` refuses existing output files.

For each subsequent release, use the same private key and a strictly newer version and build sequence:

```sh
go run ./server/cmd/release publish --root <new-payload> --base-root <old-payload> --output <release-directory> --platform windows --arch x86_64 --version 0.2.1 --sequence 2 --entry-point LinkNestX.exe --private .cache/release-signing/private.pem
go run ./server/cmd/release verify --manifest <release-directory>/windows/x86_64/latest.json --public client/update-public-key.json
go run ./server/cmd/release add-download --output <release-directory> --file <real-installer> --platform windows --arch x86_64 --version 0.2.1
```

Never regenerate over the pinned key. Producer output is `objects/{sha}`, `{platform}/{arch}/latest.json`, optionally `downloads/{filename}` and `downloads.json`. Set `LINKNESTX_RELEASE_DIR` to the absolute path of this directory; the server mounts it at `/updates/`, `/releases/`, `/downloads` and `/api/v1/releases`. Download catalog lists only existing size/hash verified artifacts; no fake package links. Publisher stores all blocks including reusable ones for arbitrary prior-version/full fallback. Generating artifacts locally does not publish them; transferring the complete output into the configured server release directory is a separate deployment step.

Installer catalog targets are separate from signed update targets: `add-download` also accepts `--platform windows7 --arch x86` and `--platform windows7 --arch x86_64` for the Windows 7 SP1 32-bit and 64-bit clients. These legacy installers do not require WebView2; the Windows 10/11 installer includes the offline WebView2 runtime installer. `windows7` is not a valid signed update manifest or `/updates/{platform}/{arch}/latest.json` target.

Update installation is separate from data reconstruction: current-user writable targets only, signed manifest and all file hashes checked again by a trusted helper; old process exits before backup/swap, local health acknowledgment and rollback journal protect failure. Existing0.1 clients lack this engine; first0.2 install is manual. Later releases are generated with this format and the same private signing key.
