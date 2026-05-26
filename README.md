# MouseBridge

在局域网内的两台 Mac 之间共享鼠标和键盘操作。

> **当前状态**：通信层 + HTTP API + 浏览器 UI 完成。真实输入注入待 `helper` 实现。

---

## 安装

需要 Go 1.22+，无 CGo 依赖，无系统库要求。

```bash
git clone <repo>
cd MouseBridge/core
go build -o mousebridge ./cmd/mousebridge/
```

---

## 快速开始

**机器 A（从机，接收控制）：**

```bash
mousebridge daemon --serve
```

**机器 B（主机，发送控制）：**

```bash
mousebridge daemon --connect <机器A的IP>
```

B 连接后，A 的日志显示配对请求和 PIN：

```
[daemon] pair_request from MachineA — PIN: 847291
```

**在 A 上接受配对：**

```bash
mousebridge pair accept
```

配对完成，两侧均显示 `paired` + `slave session started`。

---

## 架构

每台机器运行一个守护进程，持有所有状态。控制命令通过 Unix Socket 发送后立即退出。

```
mousebridge daemon
    ├── Unix Socket  (~/.mousebridge/mb-<port>.sock)  ← CLI/UI 控制
    └── TCP :<port>  单端口，P2P 与 HTTP API 共用
            首行 "MOUSEBRIDGE/1.0\n" → P2P JSON Lines
            其他                     → REST + SSE (Gin)
```

---

## 文档

| 文档 | 内容 |
|------|------|
| [docs/cli.md](docs/cli.md) | 所有命令及标志说明 |
| [docs/http-api.md](docs/http-api.md) | HTTP REST + SSE 接口参考 |
| [docs/configuration.md](docs/configuration.md) | 配置文件字段说明 |
| [docs/development.md](docs/development.md) | 本地双进程测试、开发说明 |

---

## 功能

| 功能 | 状态 |
|------|------|
| 守护进程 + Unix Socket IPC | ✅ |
| 单端口 TCP（P2P + HTTP API 多路复用） | ✅ |
| 配对：PIN 码 + 接受/拒绝 | ✅ |
| 可信设备（免 PIN 自动接受） | ✅ |
| 多设备同时连接 | ✅ |
| HTTP API + SSE 事件流 | ✅ |
| 浏览器 UI（React SPA） | ✅ |
| 快捷键配置 API | ✅ |
| 延迟统计 | ✅ |
| 真实鼠标/键盘注入 | 🔜 需要 helper |
