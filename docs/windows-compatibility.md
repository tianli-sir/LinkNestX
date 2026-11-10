# Windows 客户端兼容性 / Windows compatibility

[简体中文](#简体中文) · [English](#english)

## 简体中文

### 选择安装包

| 系统 | 客户端 | 运行环境 |
| --- | --- | --- |
| Windows 10 / 11 64 位 | 标准版 | Microsoft WebView2 |
| Windows 7 SP1 64 位 | `windows7-x64-setup.exe` | 安装包自带 Electron 22，无需 WebView2 |
| Windows 7 SP1 32 位 | `windows7-x86-setup.exe` | 安装包自带 Electron 22，无需 WebView2 |

兼容版是独立桌面应用，使用相同的聊天界面和原生文件传输核心，支持上传、下载、后台传输与 P2P。关闭窗口会保留托盘进程；从托盘选择“退出”才会停止后台服务。服务器地址由发行版本绑定。

标准版依赖的运行环境及其原生程序需要较新的 Windows，**给标准版补装 WebView2 无法解决 Windows 7 兼容问题**。请使用与系统位数匹配的完整兼容版安装包。SP1 之前的系统不支持；应安装该系统可获得的 Microsoft 更新。

### 安装与更新

1. 在“计算机 → 属性”确认系统为 Windows 7 Service Pack 1，以及 32 位或 64 位。
2. 下载管理员提供的完整兼容安装包，核对同目录 `SHA256SUMS.txt` 中的校验值。安装包包含界面运行环境，安装时不下载 WebView2。
3. 安装到当前用户目录后启动 LinkNestX。首次遇到系统防火墙提示时，允许所使用网络上的文件传输连接。
4. 更新时先完成传输，从托盘退出应用，再运行新的完整安装包。兼容版暂不使用标准版的内置增量更新，也不会自动替换成标准版。

同时提供 ZIP 便携包；必须完整解压并运行顶层的 `LinkNestX-Legacy.exe`，不要单独复制 EXE。卸载保留当前用户的传输状态与缓存。32 位和 64 位兼容版共用用户配置，请勿同时运行两个版本。

### 已知边界

Electron 22 是最后支持 Windows 7 的 Electron 主版本，已结束维护。兼容版只加载随安装包提供的本地界面，网页链接交给系统浏览器打开；仍建议具备条件的设备升级系统并使用标准版。兼容包默认未做商业代码签名，Windows 可能显示未知发布者。

自动化构建会检查双架构原生编译、进程通信、核心测试及部分已知不兼容的 Windows API 导入。**这些检查不能替代 Windows 7 SP1 实机验收。**每个发行包还需在对应位数的系统上验证：启动与登录、聊天及已读、拖放/粘贴附件、两个客户端之间的 P2P、关闭窗口继续传输、退出后恢复、文档附件与覆盖安装。请以该发行版的测试说明为准。

### 构建兼容包

在 GitHub Actions 中手动运行 **Windows 7 compatibility packages**。它生成两个未签名的安装包和 ZIP，不会创建 GitHub Release 或部署服务器。原生服务使用 `i686-win7-windows-msvc` / `x86_64-win7-windows-msvc` 专用目标及固定 nightly 工具链构建标准库；不能直接换成普通 Windows Rust 目标。

本地 Windows 构建需具备 Visual Studio C++ 工具链、Rust nightly（版本见 workflow）、`rust-src`、Node.js 和 NSIS 3。先构建共享前端和原生服务，再调用 `scripts/package-legacy-windows.ps1`。该脚本从 Electron 官方发布页下载固定版本，校验官方 SHA-256，并生成包含来源提交及原生导入报告的离线包。

## English

Use the standard client on Windows 10/11 x64. Windows 7 **SP1** requires the separate `windows7-x86` or `windows7-x64` package matching the operating system. Installing WebView2 for the standard client does not make its native executable compatible with Windows 7.

The compatibility client bundles Electron 22 and the shared native P2P service. It supports the same chat interface, uploads, downloads and background transfers. Closing the window keeps the tray service running; choose **Exit** in the tray to stop it. The release binds the server address.

The installer includes the UI runtime and does not download WebView2. Install available Microsoft updates first. Verify the package against its `SHA256SUMS.txt`. For ZIP distributions, extract the entire directory and run `LinkNestX-Legacy.exe`; copying only the executable will not work. Updates currently require a full compatibility installer: finish transfers and exit the tray service before installing. The standard client's incremental updater is not used. Uninstallation preserves user data. The x86 and x64 compatibility clients share the same user profile and must not run simultaneously.

Electron 22, the last major version supporting Windows 7, is no longer maintained. The app loads only bundled local UI assets and opens external links in the system browser. Prefer an updated operating system and the standard client where possible. Packages are unsigned unless the release explicitly states otherwise.

CI checks compilation, IPC, core tests and selected incompatible PE imports. **It does not establish Windows 7 runtime compatibility.** Validate each release on real Windows 7 SP1 x86 and x64 machines, including login, chat/read receipts, dropped and pasted attachments, multi-peer P2P, tray transfers, restart/resume, document attachments and installation over an older version.

The manually dispatched **Windows 7 compatibility packages** workflow produces both architectures without publishing a release or deploying a server. It builds the standard library for the dedicated `i686-win7-windows-msvc` / `x86_64-win7-windows-msvc` Rust targets using a pinned nightly toolchain. The packaging script downloads pinned official Electron archives, verifies their official SHA-256 values and includes build provenance and an import report.

References: [Rust Win7 targets](https://doc.rust-lang.org/rustc/platform-support/win7-windows-msvc.html), [Electron Windows 7 support](https://www.electronjs.org/blog/windows-7-to-8-1-deprecation-notice), [WebView2 Windows 7 support](https://blogs.windows.com/msedgedev/2022/12/09/microsoft-edge-and-webview2-ending-support-for-windows-7-and-windows-8-8-1/).
