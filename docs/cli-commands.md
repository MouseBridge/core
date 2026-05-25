# CLI Commands

## 架构概述

MouseBridge 使用 **daemon-first 架构**：
- **daemon**：唯一的长驻进程，持有所有 TCP 连接、配对状态、session 数据和鼠标钩子
- **控制命令**：通过 Unix Socket 向 daemon 发送一条指令后立即退出，daemon 自己输出日志

```
控制命令 (serve / connect / disconnect / ...)
        │  Unix Socket (IPC)  — 发命令，立即退出
        ▼
   Daemon 进程（前台阻塞，Ctrl+C 退出）
        ├─ TCP Server (监听传入连接)
        ├─ TCP Client (主动连接远端，支持多个)
        └─ Event Broadcaster (广播事件，输出到 daemon 日志)
```

---

## Socket 路径规则

socket 路径由端口决定：`~/.mousebridge/mb-<port>.sock`

- `--port` 未指定时取配置文件默认值（39172）
- `--socket` 可手动覆盖，优先级最高
- 本地多进程测试时用不同 `-p` 即可让多个 daemon 共存

**清理**：daemon 正常退出时自动删除 socket 文件。异常退出留下的 stale socket 在下次启动时被覆盖。

---

## 命令一览

### `mousebridge daemon`

启动守护进程，前台阻塞，Ctrl+C 触发优雅关闭（停止监听 + 断开所有连接 + 删除 socket）。

```bash
mousebridge daemon [-p <port>] [--serve] [--connect <ip> ...]
```

| 标志 | 说明 |
|------|------|
| `-p`, `--port` | TCP 端口，同时决定 socket 路径（默认 39172） |
| `--serve` | 启动后立即开始监听入站连接 |
| `--connect <ip>` | 启动后立即连接指定 IP，可重复多次 |

---

### `mousebridge serve`

告诉 daemon 开始监听 TCP 入站连接（从机角色），发完命令立即退出。

```bash
mousebridge serve [-p <port>]
```

---

### `mousebridge connect <ip>`

告诉 daemon 连接到指定 IP 的远端 daemon（主机角色），发完命令立即退出。可多次调用追加连接。

```bash
mousebridge connect <ip> [-p <port>]
```

---

### `mousebridge stop-serve`

告诉 daemon 停止监听 TCP 入站连接，已建立的连接不受影响。

```bash
mousebridge stop-serve [-p <port>]
```

---

### `mousebridge disconnect <device-id>`

告诉 daemon 断开指定设备。device-id 从 `status` 命令获取。

```bash
mousebridge disconnect <device-id> [-p <port>]
```

---

### `mousebridge pair`

管理配对请求。

```bash
mousebridge pair accept          # 接受当前待处理的配对请求
mousebridge pair reject          # 拒绝当前待处理的配对请求
mousebridge pair pin <PIN>       # 向对端提交 PIN 码
```

---

### `mousebridge status`

查询 daemon 当前连接的设备列表。

```bash
mousebridge status [-p <port>]
```

---

## IPC 协议参考

### CLI → Daemon（Command）

| `cmd` | 作用 | 附加字段 |
|-------|------|---------|
| `serve` | 开始 TCP 监听 | `port`（可选） |
| `connect` | 主动连接远端 | `ip`, `port` |
| `stop_serve` | 停止 TCP 监听 | — |
| `disconnect` | 断开指定设备 | `device_id` |
| `pair_accept` | 接受配对请求 | — |
| `pair_reject` | 拒绝配对请求 | — |
| `pair_pin` | 提交配对 PIN | `pin` |
| `status` | 查询设备状态 | — |

### Daemon → CLI（Event）

| `event` | 含义 | 附加字段 |
|---------|------|---------|
| `listening` | 开始监听 | `port` |
| `connected` | 连接建立 | `device_id`, `name`, `ip` |
| `pair_request` | 收到配对请求 | `device_id`, `name`, `pin` |
| `paired` | 配对成功 | `device_id`, `name` |
| `disconnected` | 连接断开 | `device_id`, `name` |
| `status` | 设备列表快照 | `devices[]` |
| `log` | 普通日志 | `msg` |
| `error` | 错误消息 | `msg` |
