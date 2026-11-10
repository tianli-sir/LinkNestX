# 开发指南 / Development guide

[简体中文 README](../README.md) · [English README](../README.en.md)

服务端和离线签名工具支持 Linux，macOS 用于本地开发；Windows 仅构建客户端。Win7 兼容构建见 [Windows 兼容性](windows-compatibility.md)。

The server and offline signing tool target Linux, with macOS for local development. Windows builds target the desktop client; see [Win7 compatibility](windows-compatibility.md).

本指南补充双语 README 中的快速开始，记录构建、验证和打包方式。以下命令在仓库根目录的 Bash 终端运行；Windows 使用 Git Bash。

This guide expands on the bilingual README with build, test, and packaging details. Run commands from the repository root in Bash; use Git Bash on Windows.

## 工具链 / Toolchains

| 工具 / Tool | 源码要求 / Source requirement | 本地验证版本 / Locally verified version |
| --- | --- | --- |
| Go | 1.26.0+ | 1.27.2 |
| Node.js | 22+ | 24.14.0 |
| npm | 10+ | Use the version supplied with the verified Node.js installation |
| Rust | 1.90+ for the locked Tauri dependencies | 1.99.1 |

依赖由 `server/go.sum`、`client/package-lock.json` 与 `client/src-tauri/Cargo.lock` 锁定。最低版本是声明要求，不代表所有中间版本均已测试。Use the lockfiles; declared minimum versions do not mean every intermediate version has been tested.

- **macOS:** 安装 Xcode Command Line Tools。安装包配置最低 macOS 11，但兼容性仍需对应实机验证。Install Xcode Command Line Tools. The configured deployment target is macOS 11; this is not a claim of testing on every supported OS version.
- **Windows:** 安装 Visual Studio Build Tools 的 C++ 桌面开发工作负载、Windows SDK、Rust MSVC 工具链与 WebView2。Install the Visual Studio C++ desktop workload, Windows SDK, the Rust MSVC toolchain, and WebView2.
- **Web only:** Go + Node.js/npm 即可启动服务和网页；无需 Rust。Rust is not required to run the server and web client.

```bash
npm --prefix client ci
```

通常使用系统 Rust。若系统找不到 Cargo，Makefile 和打包脚本也能使用预装在 `.cache/cargo/bin` 与 `.cache/rustup` 的本地工具链，同时设置 `CARGO_HOME`、`RUSTUP_HOME` 和 `PATH`，仅作用于子进程。它们不会替你下载安装工具链。

The scripts prefer system Rust. If Cargo is absent, they can use an already installed repository-local toolchain at `.cache/cargo/bin` and `.cache/rustup`, with both toolchain homes and `PATH` scoped to the child process. They do not install that toolchain automatically.

## 本地开发 / Local development

首次运行的管理员设置与 Web 入口见 [README 快速开始](../README.md#quick-start) / [English quick start](../README.en.md#quick-start)。

```bash
./scripts/dev-server.sh
```

脚本默认监听 `127.0.0.1:8080`，数据写入 `server/data/`；保留已有 `LINKNESTX_*` 配置，不生成密码或 JWT 密钥。程序不会自动读取 `.env`。首次启动需同时设置管理员用户名与密码，密码至少八个字符；已有管理员时不会覆盖账号。员工账号通过桌面管理界面或受认证 API 创建。

The script defaults to `127.0.0.1:8080` and `server/data/`, preserves existing `LINKNESTX_*` settings, and does not generate credentials. It does not load `.env`. Set both bootstrap account variables for first initialization, with a password of at least eight characters. Existing administrators are not overwritten. Create employee accounts through the desktop management interface or authenticated API.

浏览器与服务必须同源。构建后配置 Web 资产目录，再访问服务端 `/app/`：

The browser application and API must share an origin. Build the UI and configure its assets before starting the server:

```bash
npm --prefix client run build
export LINKNESTX_WEB_ASSETS_DIR="$PWD/client/dist"
./scripts/dev-server.sh
```

桌面客户端读取 [`SERVER_ORIGIN`](../client/src-tauri/src/deployment.rs) 的构建期地址，运行时不能编辑。开发时将其调整为自己的本机服务地址，再另开终端启动：

The desktop app uses the build-time `SERVER_ORIGIN`, with no runtime server field. Point it at your local server before starting it in a second terminal:

```bash
make dev-client
# 或 / or:
npm --prefix client run desktop:dev
```

Vite 使用 `127.0.0.1:1420`，Tauri 提供原生文件选择、传输与系统集成。不要把临时开发地址作为企业发布配置。For the web experience, use the server's `/app/` endpoint; Vite's desktop development address alone is not a substitute for same-origin web authentication.

## 自动检查 / Automated checks

```bash
make test          # Go + frontend + native Rust tests
make check-client  # TypeScript + production frontend build
make smoke         # Real Go server + native multi-source integration
make check         # All of the above + shell harness checks
```

缺工具或任何失败都会返回非零状态。无 Make 时可依次运行：

Missing tools or failed checks return a nonzero status. Without Make, run:

```bash
go test -mod=readonly ./server/...
npm --prefix client test
npm --prefix client run build
cargo test --locked --manifest-path client/src-tauri/Cargo.toml
node --test scripts/test-smoke-script.mjs
./scripts/smoke-test.sh
```

服务端或原生代码变更还应运行相关静态、竞态与格式检查：

For relevant server or native changes, also run static, race, and formatting checks:

```bash
go test -mod=readonly -race ./server/...
go vet -mod=readonly ./server/...
cargo fmt --manifest-path client/src-tauri/Cargo.toml -- --check
```

### 烟测验证什么 / What the smoke tests verify

脚本编译并启动独立临时 Go 服务，通过真实认证、原生传输与 WebRTC 节点验证：

The smoke script builds an isolated Go service and uses real authentication, native transfers, and WebRTC peers to exercise:

- 账号初始化、群聊与群文件提升到公共池。Account setup, group conversations, and file promotion to the shared pool.
- 48 MiB 文件从两个客户端与服务器共同下载，验证来源、审计字节和完整哈希。A 48 MiB download from two peers plus the server, checking source contributions, audit bytes, and the final hash.
- 未完成文件的已校验分块转发，服务重建后的续传，以及撤销设备与缓存清理边界。Serving verified chunks before completion, resuming after service recreation, device revocation, and safe cache cleanup.
- 205 条审计记录跨批次完整导出 CSV/JSON。Complete CSV/JSON exports across batches for 205 audit records.

测试只绑定本机，清理自己启动的进程和临时数据；运行环境需要允许本机 TCP/UDP。常规 Rust 的 20 GB 测试检查 4,769 个 4 MiB 分块的清单几何及超限拒绝，**不是实际 20 GB 传输**。平台安装、真实跨 NAT 和完整 GUI 升级需要单独验收。

Tests bind only to loopback and clean up their own processes and data. Local TCP/UDP must be allowed. The regular 20 GB test covers manifest geometry and size rejection, **not a physical 20 GB transfer**. Platform installation, cross-NAT networks, and full GUI upgrades require separate acceptance.

## 构建与打包 / Build and package

服务端 / Server:

```bash
go build -mod=readonly -o server/linknestx ./server/cmd/server
```

在目标桌面平台构建测试包 / Build a test package on the target desktop platform:

```bash
./scripts/package-client.sh --dry-run
./scripts/package-client.sh --unsigned

# macOS: .app only
./scripts/package-client.sh --unsigned --bundles app

# Windows, in Git Bash
bash scripts/package-client.sh --unsigned --bundles nsis,msi
```

打包先构建前端，再使用 Cargo 锁文件构建原生程序。默认产物位于 `client/src-tauri/target/release/bundle/`；指定 `--target` 后位于 `client/src-tauri/target/<target>/release/bundle/`。

Packaging builds the frontend first, then builds the native app with the Cargo lockfile. Artifacts are placed under the paths above. The default macOS bundles are `.app` and `.dmg`; Windows defaults to a per-user NSIS installer.

`--unsigned` 在 Windows 不使用签名；macOS 使用本机 ad-hoc 签名封装完整应用，**不包含 Developer ID 或公证**。正式平台签名、离线 WebView2 和持久化服务配置见[部署文档](deployment.md)。

`--unsigned` disables signing on Windows and uses an ad-hoc bundle seal on macOS. It does **not** provide Developer ID signing or notarization. See the [deployment guide](deployment.md) for release signing, offline WebView2, and persistent service setup.

构建结果以对应提交的 Actions 为准；Windows/macOS 实机验收仍需分别完成。Read Actions for the corresponding commit for build status; each platform still needs device acceptance.

### 更新发布 / Publishing updates

首次从 0.1 升级到 0.2 需手动安装。后续发布须同步 npm、Cargo、Tauri 版本并提升更新序号，使用自己的离线签名密钥与对应客户端公钥。正式密钥不得提交到仓库、打包进客户端或上传到公开服务。

The first upgrade from 0.1 to 0.2 is manual. Subsequent releases must advance both the application version and build sequence, keep npm/Cargo/Tauri versions aligned, and use the deployment's own offline signing key and matching embedded public key. Never commit or distribute private signing keys. See [update format](update-format.md) and [desktop update behavior](update-client.md).

## 持续集成 / Continuous integration

[`Checks`](../.github/workflows/checks.yml) 在 Linux/macOS 上运行服务端测试，在 macOS 上执行真实服务烟测，在 Windows/macOS 上运行前端与原生客户端测试及构建。[`Unsigned desktop packages`](../.github/workflows/desktop-packages.yml) 只在手动触发时打包，保留测试产物 14 天，不创建 GitHub Release，也不部署服务。

`Checks` runs server tests on Linux/macOS, real-server smoke tests on macOS, and frontend/native client tests and builds on Windows/macOS. `Unsigned desktop packages` is manually triggered, retains test artifacts for 14 days, and neither publishes a GitHub Release nor deploys a server. Workflow configuration is not evidence of a successful remote run.

<a id="contributing"></a>
## 贡献方式 / Contributing

1. 在 Issue 中说明场景、预期与实际行为，附复现步骤及平台版本。Describe the use case, expected and actual behavior, reproduction steps, and platform versions.
2. 让每个 Pull Request 聚焦一个问题，保持接口、迁移与权限语义一致。Keep each Pull Request focused and preserve API, migration, and authorization semantics.
3. 对行为变更运行适当检查，说明实际通过的验证和未验证平台。Run relevant checks and distinguish verified behavior from platform gaps.
4. 保护已有数据和环境；提交前检查差异，排除凭据、私钥、员工数据、缓存与安装包。Review the diff and exclude credentials, private keys, employee data, caches, and generated installers.

界面改动请附相应尺寸的截图，注明演示数据或真实测试数据。传输、鉴权与更新改动应提供行为验证，不能只靠界面截图。For UI changes, include appropriately sized screenshots and identify sample data. Transfer, authentication, and updater changes need behavioral evidence beyond screenshots.
