# MouseBridge Core

在局域网内转发鼠标和键盘输入的守护进程与 HTTP API。

> 当前状态：`core` + `helper` 已能完成真实输入捕获、远端转发、远端注入、热键切换、边缘切换。推荐使用 Web UI 管理；CLI 目前只保留 `daemon` 作为稳定入口，其余命令仍在补完。

## 安装

需要 Go 1.22+。

```bash
git clone <repo>
cd MouseBridge/core
go build -o mousebridge ./cmd/mousebridge/
```

## 快速开始

1. 在两台 Mac 上都启动 daemon：

```bash
mousebridge daemon
```

2. 在发送端打开 Web UI，连接到本机 daemon，然后在 Devices 页面发起 `Connect` 到接收端 `ip:port`。

3. 接收端会生成 6 位 PIN；发送端输入 PIN 完成配对。

4. 在两台机器上都启动 `mousebridge-helper`。
推荐直接安装 LaunchAgent，见 [`../helper/README.md`](../helper/README.md)。

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
