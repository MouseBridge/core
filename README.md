# MouseBridge Core

在局域网内转发鼠标和键盘输入的守护进程与 HTTP API。

> 当前状态：`core` + `helper` 已能完成真实输入捕获、远端转发、远端注入、热键切换、边缘切换。推荐使用 Web UI 管理；CLI 目前只保留 `daemon` 作为稳定入口。Web UI 现可检查 helper 就绪度、安装/重启 LaunchAgent、打开 Accessibility 设置，并可直接触发本机 `local validation suite`。

## 安装

需要 Go 1.22+。

```bash
git clone <repo>
cd MouseBridge/core
go build -o mousebridge ./cmd/mousebridge/
```

## 快速开始

1. 在两台 Mac 上都先启动 daemon：

```bash
mousebridge daemon
```

2. 在两台机器上分别打开 Web UI 的 `Setup` 页面，先完成：

- daemon 已连接
- daemon 可被另一台机器访问
- helper 已安装 LaunchAgent
- helper 已授予 Accessibility
- helper 已连回 daemon

如果还没上双机，先在其中一台机器打开 Web UI 的 `Validation` 页面，跑一次单机双端验证，确认 smoke / anti-loop / batching-latency 都通过，再进入真实双机实验。

3. 两台都准备好后，在发送端的 `Devices` 页面发起 `Connect` 到接收端 `ip:port`。

4. 接收端会生成 6 位 PIN；发送端输入 PIN 完成配对。

5. 使用默认紧急返回键 `ctrl+alt+escape` 验证能随时回到本机；快捷键可在 `Settings` 页面调整。

注意：

- Web UI 依赖 daemon 提供，所以 daemon 仍需要先启动，UI 不能从零启动 daemon
- daemon 首次使用新的 `data-dir` 时会自动写出 `config.json`，供 helper 与 LaunchAgent 直接复用
- 如果要做双机实验，daemon 不能只监听 loopback；需要把 `listen_host` 改成对端可访问的地址，例如 `0.0.0.0`，并显式开启 `unsafe_http_lan=true`
- helper 仍然是独立二进制，安装方式见 [`../helper/README.md`](../helper/README.md)

## 运行模型

```text
mousebridge daemon
    ├── TCP :<port>          P2P + HTTP API 共用
    ├── /api/*               Web UI / 调试接口
    └── Unix socket          本地 helper IPC
```

`helper` 负责：

- 捕获本机键盘鼠标
- 上报热键、边缘切换、输入事件
- 把远端输入真正注入到 macOS

## 当前能力

| 能力 | 状态 |
|------|------|
| 守护进程 + 单端口 P2P/HTTP | ✅ |
| PIN 配对 + remembered 自动重连 | ✅ |
| Web UI 状态面板与设备管理 | ✅ |
| Web UI setup / readiness 引导 | ✅ |
| Web UI 本机 validation 页面 | ✅ |
| 快捷键配置与推送 helper | ✅ |
| 会话断开 / remembered 重命名 | ✅ |
| 真实鼠标键盘捕获与注入 | ✅ |
| 紧急返回键 `ctrl+alt+escape` | ✅ |
| LaunchAgent 托管 helper | ✅ |
| 完整 CLI 运维面 | 进行中 |

## 文档

- [docs/http-api.md](docs/http-api.md)
- [docs/cli.md](docs/cli.md)
- [docs/configuration.md](docs/configuration.md)
- [docs/development.md](docs/development.md)
