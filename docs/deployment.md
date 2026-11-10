# 部署与桌面打包

## 服务端配置

以 `server/internal/config/config.go` 的实现为准。变量通过服务管理器或秘密管理工具注入；程序和开发脚本不会自动读取 `.env` 文件。共享部署不要使用默认开发 JWT 密钥，不要把密码或 TURN 共享秘密写入代码、命令日志或提交到仓库。

| 环境变量 | 默认值 / 用途 |
| --- | --- |
| `LINKNESTX_BIND_ADDRESS` | `127.0.0.1:8080`，HTTP 监听地址 |
| `LINKNESTX_STORAGE_ROOT` | `./data`，分块存储目录 |
| `LINKNESTX_SQLITE_PATH` | `./data/linknestx.sqlite3`，SQLite 文件路径 |
| `LINKNESTX_WEB_ASSETS_DIR` | 默认为空；指向前端生产构建目录的绝对路径，为 `/app/` 提供网页界面 |
| `LINKNESTX_RELEASE_DIR` | 默认为空；指向离线发布工具的输出目录，提供客户端安装包和签名更新资产 |
| `LINKNESTX_JWT_SIGNING_KEY` | 内置开发值；共享部署必须提供独立随机访问令牌密钥 |
| `LINKNESTX_JWT_REFRESH_KEY` | 内置开发值；共享部署必须提供不同的随机刷新令牌密钥 |
| `LINKNESTX_VERSION` | `dev`，健康检查版本标识 |
| `LINKNESTX_BOOTSTRAP_ADMIN_USERNAME` | 无默认值，首次管理员用户名 |
| `LINKNESTX_BOOTSTRAP_ADMIN_PASSWORD` | 无默认值，与用户名一起设置，至少八个字符；创建后移除 |
| `LINKNESTX_CLEANUP_INTERVAL` | `1h`，清理间隔，必须为正值 |
| `LINKNESTX_TRUSTED_PROXY_CIDRS` | 默认为空；逗号分隔的可信反向代理 CIDR，最多 32 个 |
| `LINKNESTX_ICE_SERVERS` | `[]`，ICE 服务 JSON 数组，可留空用于局域网环境 |
| `LINKNESTX_TURN_SHARED_SECRET` | 无默认值，TURN 临时凭据的共享秘密 |
| `LINKNESTX_TURN_CREDENTIAL_TTL` | `10m`，范围 `1m` 至 `1h` |

相对数据路径相对于服务进程的工作目录；生产部署请使用绝对路径。开发脚本将默认路径调整为仓库内 `server/data/`，并保留调用者传入的路径。

代码仍接受 `LINKNESTX_FILE_RETENTION`、`LINKNESTX_MESSAGE_RETENTION`、`LINKNESTX_AUDIT_RETENTION` 三个历史配置项，但当前上传和清理流程不以这些值改变产品保留规则，不应依赖它们调整保留期。普通文件自上传完成起 30 天后到期；公共池文件永久保留，由管理员管理；聊天及审计记录按 UTC 日期减两个日历年清理。时长变量使用 Go 时长语法，如 `1h`、`10m`，不接受 `30d`。

首次管理员只由启动配置创建。已有管理员时，重复提供启动凭据不会重置其密码。管理员通过桌面管理界面或受认证 API 创建员工账号。0.2 提供 `/app/` 员工网页登录入口；浏览器会话可聊天、查看好友和已读、下载单个不超过 1 GB 的文件，不能上传、访问原生 P2P 分片接口或执行管理写操作。业务文件没有匿名分享入口；公开的 `/downloads` 只提供实际发布的客户端安装包。禁用账号、移除群成员、撤销设备后，服务端重新检查后续访问权限。

## 数据目录与运行账户

使用专用非管理员操作系统账户运行服务。该账户需要读写数据库目录、SQLite 的 WAL/SHM 文件和整个分块存储目录；其他普通账户不应有读取权限。Unix 上建议目录权限 `0700`、文件权限 `0600` 并设置 `umask 077`；Windows 上用 NTFS ACL 仅授予服务账户和负责备份的管理员。不要把数据目录放进反向代理的静态站点根目录。

备份必须同时覆盖数据库和分块存储，并保持一致的时间点；可以停服务备份，或采用 SQLite 在线备份并配套保留对应文件。先验证可恢复性，再调整清理或迁移流程。客户端也有本地任务数据库、会话数据和文件缓存，位于 Tauri 的应用数据目录；不要将其放进共享目录或同步给其他员工。

从仓库根目录构建服务：

```sh
go build -mod=readonly -o server/linknestx ./server/cmd/server
```

服务管理器应设置工作目录、上述环境变量和数据目录权限，启动构建产物，并在重启前让旧进程完成关闭。健康探测只请求 `/healthz`；业务 API 需要员工凭据。

## 网页与客户端下载

在仓库根目录构建前端：

```sh
npm --prefix client ci
npm --prefix client run build
```

将生成的 `client/dist/` 内容复制到服务账户可读的独立目录，例如 `/opt/linknestx/web/`，并设置 `LINKNESTX_WEB_ASSETS_DIR=/opt/linknestx/web`。通过同一个服务访问 `/app/`，浏览器 API 和 WebSocket 使用同源会话。不要直接用文件方式打开 `dist/index.html`，也不要把整个代码仓库作为静态资源根目录。未配置网页资源目录时，`/app/` 返回 404；内嵌官网 `/` 仍可访问。

客户端安装包和更新清单由[离线发布工具](update-format.md)生成。将其完整输出目录复制到例如 `/opt/linknestx/releases/`，设置 `LINKNESTX_RELEASE_DIR=/opt/linknestx/releases`。`/downloads` 展示经大小和散列验证的真实安装包，`/api/v1/releases` 提供同一目录的列表，`/updates/` 提供签名清单和内容对象。目录为空时不会伪造安装包链接。发布目录只包含公开资产，不放入更新签名私钥、业务文件或数据库。

从旧版升级前备份数据和现有程序，部署匹配版本的服务程序、网页资源及发布资产。服务启动时执行数据库迁移；如需回滚数据，恢复同一时间点的数据库与文件备份。0.1 客户端没有自动更新器，首次升级到 0.2 需手动安装。

## HTTPS 与反向代理

公网访问地址使用 HTTPS，WebSocket 对应 WSS；受信任局域网可显式使用私有 IPv4 的 HTTP 地址，具体范围见下文。Go 服务可保持监听回环地址，由同机反向代理对外提供连接。以下 Nginx 示例用于公网 HTTPS，需要替换真实域名和管理员安装的证书路径；不包含证书申请或公网部署操作。

```nginx
# http {} 内
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name files.example.com;
    ssl_certificate     /etc/ssl/linknestx/fullchain.pem;
    ssl_certificate_key /etc/ssl/linknestx/privkey.pem;

    # 文件按分块传输；为协议请求留余量，避免代理缓冲完整文件。
    client_max_body_size 64m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_request_buffering off;
        proxy_buffering off;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
```

同机 Nginx 部署可将 `LINKNESTX_TRUSTED_PROXY_CIDRS` 设为 `127.0.0.1/32,::1/128`。仅配置实际代理来源，避免信任整个员工网段。服务只对来自可信代理的连接读取单个 `X-Forwarded-For` 请求头，并从右向左查找第一个不可信跳点作为客户端地址；代理必须追加真实来源地址（上例的 `$proxy_add_x_forwarded_for`）或覆盖客户端提供的该请求头。浏览器会话还依赖来自可信代理的单个 `X-Forwarded-Proto: https` 判断外部协议并设置安全 Cookie；上例同时保留外部 `Host`。默认不信任代理请求头；未配置时，代理后的用户会共用代理地址的登录限流预算。

保留 `Authorization` 请求头，确保聊天与 `/api/v1/p2p/ws` 的 WebSocket 升级可用。不要把完整 JWT 或含凭据的环境打印到日志。只开放实际需要的网络端口，公网员工连接 TLS 域名。桌面客户端允许显式 HTTP 连接本机地址及 RFC1918 私有 IPv4 地址（10/8、172.16/12、192.168/16），适用于用户信任的局域网；登录及服务端通信为明文。公网 IP、域名和其他地址仍要求 HTTPS；省略协议也默认 HTTPS。公网部署使用 TLS 域名和受信任证书。

## 可选 STUN/TURN

没有外部 STUN/TURN 时，`LINKNESTX_ICE_SERVERS` 默认 `[]`；这不保证跨 NAT 直连。协议支持发现候选源、协商直连及 HTTP 服务端回退，真实网络连通性、原生数据通道和 TURN 中继仍需在目标网络验收。

配置格式示例（域名由管理员替换）：

```json
[
  {"urls": ["stun:turn.example.com:3478"]},
  {"urls": ["turn:turn.example.com:3478?transport=udp", "turns:turn.example.com:5349?transport=tcp"]}
]
```

将此 JSON 作为 `LINKNESTX_ICE_SERVERS` 的值，并在服务端秘密配置中设置 `LINKNESTX_TURN_SHARED_SECRET`。TURN 服务需启用兼容 TURN REST 的 HMAC-SHA1 临时凭据，配置相同共享秘密及正确 realm，并允许客户端访问监听端口和运营者设定的中继端口范围。服务端返回临时用户名 `<到期Unix秒>:<设备UUID>` 和派生凭据，不向客户端返回共享秘密。

私有 TURN 也可以在单条配置中使用成对的 `username`、`credential`；设置共享秘密后，TURN 项使用派生临时凭据。`LINKNESTX_TURN_CREDENTIAL_TTL` 默认 `10m`，最短 `1m`、最长 `1h`。不要让静态凭据或共享秘密出现在公开配置示例中。证书签名、HTTPS 代理和 TURN 服务是不同职责，应用打包不会自动部署它们。

## 平台构建与签名

运行 `npm ci` 使用已提交的锁文件；Tauri CLI 是本地依赖，无需全局安装。发布构建先执行 `npm run build`，再使用 `cargo --locked`，禁止以修改锁文件的方式隐式升级依赖。版本号目前在前端包、Tauri 配置和 Cargo manifest 中均为 `0.2.0`；正式升级需同步版本及对应锁文件，并递增 `client/src-tauri/src/update/manifest.rs` 中的 `BUILD_SEQUENCE`。

打包前，将 `client/src-tauri/src/deployment.rs` 的 `SERVER_ORIGIN` 设置为自己的服务地址，例如 `https://files.example.com`。该地址是构建期绑定，员工不能在登录界面修改；只改变服务端环境变量不会改变已有客户端的连接地址。独立部署还应在第一次构建前建立自己的更新签名密钥，并将对应公钥写入 `client/update-public-key.json`，详见[更新格式](update-format.md)。已有安装必须继续信任同一发布密钥；不要用重新生成公钥的方式替换已分发客户端的信任关系。

`scripts/package-client.sh --unsigned` 在 Windows 传入 `--no-sign`；macOS 使用不需要证书的本机 ad-hoc 签名封装完整 `.app`，以便更新器验证资源完整性，没有 Developer ID 签名或公证。macOS 默认 `.app`/`.dmg`，Windows 默认 NSIS `.exe`，需要 MSI 时传 `--bundles nsis,msi`。目标架构默认采用当前主机；跨架构构建必须先安装目标工具链，例如 macOS 通用包需要两个 Apple 架构的 Rust target，再传 `--target universal-apple-darwin`。脚本可从任意工作目录调用，失败状态直接返回给调用者，不删除旧产物。

Windows 安装包配置采用当前用户安装及简体中文/英文界面，WebView2 采用在线 bootstrapper；离线环境需由管理员预装 WebView2 或使用经验证的离线分发配置。macOS 配置最低版本为 11.0；这个配置值不是旧版本系统测试结果。

macOS 正式签名需先将合法的 Developer ID 证书和私钥安装到构建账户钥匙串，通过 `APPLE_SIGNING_IDENTITY` 指定身份，然后运行：

```sh
./scripts/package-client.sh --signed
```

公证凭据通过 Tauri 支持的环境变量提供：App Store Connect API 的 `APPLE_API_ISSUER`、`APPLE_API_KEY`、`APPLE_API_KEY_PATH`，或 Apple ID 路径的 `APPLE_ID`、`APPLE_PASSWORD`（应用专用密码）、`APPLE_TEAM_ID`。仅配置签名身份不等于完成公证；检查构建日志和 `codesign`、`spctl`、`stapler` 的实际验证结果。仓库没有任何证书或公证账号。

Windows 签名需在构建账户证书存储中安装合法证书及可访问的私钥，或配置组织认可的外部签名服务。设置 `LINKNESTX_SIGNING_CONFIG` 为工作区之外的 Tauri JSON 覆盖文件路径，传入 `--signed`。例如使用证书存储时，该文件的结构为：

```json
{
  "bundle": {
    "windows": {
      "certificateThumbprint": "由秘密配置注入真实证书指纹",
      "digestAlgorithm": "sha256",
      "timestampUrl": "由证书服务商提供的时间戳地址"
    }
  }
}
```

这只是结构示例，不能直接用于签名。通过秘密管理工具生成实际文件，不在命令行插值或仓库中存放密钥。脚本将配置路径作为单独参数传给 Tauri，不执行配置文本，也不输出配置内容。签名是否成功以 `Get-AuthenticodeSignature` 等平台验证结果为准。

## 持续集成与验收范围

`Checks` 固定 Go 1.27.2、Node.js 24.14.0、Rust 1.99.1，在 `macos-14` 和 `windows-2025` 运行 Go/前端/原生测试，执行前端生产构建和原生无安装包构建。所有安装使用 `npm ci`，Cargo 使用 `--locked`，Go 使用 `-mod=readonly`。

`Unsigned desktop packages` 只在手动触发时构建 macOS ARM64 与 Windows x64 未签名安装包，产物保留 14 天。它不使用签名秘密、不创建 GitHub Release、不自动部署。Actions 尚需在仓库远程实际运行，不能从本地配置检查推断它已通过。

发布前还需在目标 Windows/macOS 上实际安装、登录、后台传输、托盘退出、升级/卸载，验证管理员和普通员工权限、两个日历年清理边界、文件 30 天到期、公共池管理及 20 GB 上限，并在预期 NAT/TURN 网络中验证传输和回退。记录实际版本、架构、构建与测试结果；未签名本机编译不代替这些验收。
