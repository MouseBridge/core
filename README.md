# MouseBridge

在局域网内的两台 Mac 之间共享鼠标和键盘操作。

> **当前状态：通信层 + HTTP API + 浏览器 UI 完成**
> 已实现守护进程架构 + 设备配对 + 单端口 P2P/HTTP 多路复用 + 延迟日志 + 可信设备持久化 + 浏览器 UI。
> 尚未实现真实的输入注入，鼠标/键盘事件目前仅做记录和转发。

---

## 安装

```bash
git clone <repo>
cd MouseBridge
go build -o mousebridge ./cmd/mousebridge/
```

需要 Go 1.22+。无 CGo 依赖，无系统库要求。

---

## 架构概览

每台机器运行一个前台守护进程（`mousebridge daemon`），所有状态由守护进程持有。控制命令通过 Unix Socket 向守护进程发送指令后立即退出，日志由守护进程自己输出。

```
mousebridge daemon              ← 守护进程（前台阻塞，Ctrl+C 退出）
    │
    ├── Unix Socket (~/.mousebridge/mb-<port>.sock)
    │       ↑
    │   mousebridge serve / connect / disconnect / status / pair
    │   （发完命令立即退出）
    │
    └── TCP :<port>  ← 单端口，P2P 与 HTTP API 共用
            │  首行 "MOUSEBRIDGE/1.0\n" → P2P JSON Lines 协议
            │  其他（HTTP 请求）        → 浏览器 UI（REST + SSE）
            ↕
    对端 mousebridge daemon（另一台机器）
```

socket 路径按端口自动区分（`mb-39172.sock`），本地多进程测试时用不同 `-p` 即可让多个 daemon 共存。

---

## 快速开始

### 两台机器互联

**机器 A（从机，接收控制）：**

```bash
mousebridge daemon --serve
```

**机器 B（主机，发送控制）：**

```bash
mousebridge daemon --connect <机器A的IP>
```

连接建立后，两侧守护进程日志均显示配对请求：

```
[daemon] pair_request from MachineA, PIN=847291
```

**在机器 A 上接受配对：**

```bash
mousebridge pair accept
```

配对完成后双方守护进程均显示：

```
[daemon] paired with MachineA (accept)
[daemon] slave session started with MachineA
```

---

### 运行时追加连接

daemon 已启动后，随时可以追加新的连接，不需要重启：

```bash
mousebridge connect 192.168.1.6    # 追加连接第二台设备
mousebridge stop-serve             # 停止监听（不断开已有连接）
mousebridge disconnect <device-id> # 断开某台设备
mousebridge status                 # 查看当前连接
```

---

### 本地双进程测试

```bash
# 终端 1 — 进程 A（从机），单端口 39172（P2P + HTTP API 共用）
mousebridge daemon --serve -p 39172

# 终端 2 — 进程 B（主机），单端口 39174
mousebridge daemon -p 39174

# 终端 3 — 等两个 daemon 都打印 socket: 后再执行

# 让 B 连接 A（-p 指定 B 的 daemon socket，--target-port 指定 A 的 TCP 端口）
mousebridge connect 127.0.0.1 -p 39174 --target-port 39172

# A 会打印 pair_request，含 PIN（只在 A 本地可见，不通过网络传输）
# B 会打印 pair_request，提示从远端设备获取 PIN

# 在 A 上 accept（从机侧）
mousebridge pair accept -p 39172

# 在 B 上提交 A 显示的 PIN（主机侧）
mousebridge pair pin <PIN> -p 39174

# 查看状态
mousebridge status -p 39172
mousebridge status -p 39174
```

#### 信任设备（后续连接免 PIN）

配对成功后，可将对方加入信任列表。下次连接时 A 将自动接受，无需 PIN。

```bash
# 在 A 上信任 B（device-id 从 status 或 pair_request 日志获取）
mousebridge trust add <B-device-id> --name "My-MacBook-B" -p 39172

# 查看已信任设备
mousebridge trust list -p 39172

# 再次连接时 A 将自动 accept，B 侧无需 pair pin 步骤
mousebridge connect 127.0.0.1 -p 39174 --target-port 39172

# 撤销信任
mousebridge trust remove <B-device-id> -p 39172
```

---

## 命令参考

所有命令支持 `-p <port>` 指定目标 daemon（通过 socket 路径匹配），`--socket <path>` 可直接覆盖 socket 路径。

### `mousebridge daemon`

启动守护进程，前台阻塞。Ctrl+C 触发优雅关闭：停止监听、断开所有连接、删除 socket 文件。

```bash
mousebridge daemon [-p <port>] [--serve] [--connect <ip> ...]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-p`, `--port` | TCP 端口（P2P 设备互联 + HTTP API），同时决定 socket 路径 | 39172 |
| `--serve` | 启动后立即开始监听 | — |
| `--connect <ip>` | 启动后立即连接，可重复多次 | — |

### `mousebridge serve`

告诉 daemon 开始监听 TCP 入站连接（从机角色），发完命令立即退出。

### `mousebridge connect <ip>`

告诉 daemon 连接到远端 daemon（主机角色），发完命令立即退出。可多次调用追加连接。

### `mousebridge stop-serve`

告诉 daemon 停止监听，已有连接不受影响。

### `mousebridge disconnect <device-id>`

告诉 daemon 断开指定设备。device-id 从 `status` 输出获取。

### `mousebridge pair accept|reject|pin`

管理配对请求。`accept`/`reject` 在从机侧执行，`pin <PIN>` 在主机侧执行。

### `mousebridge status`

查看当前连接设备列表及延迟统计。

### `mousebridge trust add|remove|list`

管理信任设备列表（持久化到 `~/.mousebridge/trusted.json`）。

- `trust add <device-id> [--name <name>]` — 信任指定设备，后续连接自动免 PIN
- `trust remove <device-id>` — 撤销信任
- `trust list` — 列出所有已信任设备

---

## HTTP API

daemon 启动后（需先 `--serve` 或 `mousebridge serve`），HTTP API 与 P2P 共用同一端口，路径前缀 `/api`。

### 设备控制

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/serve` | `{"port": 0}` | 开始监听入站连接（port 为 0 时用配置默认值） |
| `POST` | `/api/stop-serve` | — | 停止监听 |
| `POST` | `/api/connect` | `{"ip": "192.168.1.6", "port": 39172}` | 连接到远端 daemon |
| `POST` | `/api/disconnect` | `{"device_id": "..."}` | 断开指定设备 |
| `GET`  | `/api/status` | — | 当前状态快照（serving、port、devices） |

### 配对

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/pair/accept` | `{"device_id": "..."}` | 接受配对请求（从机侧） |
| `POST` | `/api/pair/reject` | `{"device_id": "..."}` | 拒绝配对请求（从机侧） |
| `POST` | `/api/pair/pin` | `{"pin": "123456"}` | 提交 PIN（主机侧） |

### 信任设备

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET`    | `/api/trusted` | — | 列出所有已信任设备 |
| `POST`   | `/api/trusted` | `{"device_id": "...", "name": "..."}` | 添加信任 |
| `DELETE` | `/api/trusted` | `{"device_id": "..."}` | 撤销信任 |

### 快捷键

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/shortcuts` | — | 读取当前快捷键配置 |
| `PUT` | `/api/shortcuts` | `{"switch_right": "ctrl+alt+right", ...}` | 更新快捷键并推送到 helper |

### 事件流（SSE）

```
GET /api/events
```

长连接，返回 `text/event-stream`。每个事件为 `event: message\ndata: <JSON>\n\n`。

| `event` 字段 | 触发时机 | 主要字段 |
|-------------|---------|---------|
| `listening` | 开始监听 | `port` |
| `pair_request` | 收到配对请求 | `device_id`, `name`, `pin`（从机侧有 pin），`role` |
| `paired` | 配对完成 | `device_id`, `name` |
| `connected` | 设备连接建立 | `device_id`, `name`, `ip` |
| `disconnected` | 设备断开 | `device_id`, `name` |
| `status` | status 查询响应 | `devices[]` |
| `trusted_list` | trusted list 查询响应 | `devices[]` |
| `log` | 普通日志 | `msg` |
| `error` | 错误 | `msg` |

---

## 配置文件

路径：`~/.mousebridge/config.json`，不存在时自动使用默认值。

```json
{
  "port": 39172,
  "device_name": "MyMacBook",
  "hotkeys": {
    "switch_right": "ctrl+alt+right",
    "switch_left": "ctrl+alt+left"
  }
}
```

| 字段 | 说明 |
|------|------|
| `port` | TCP 端口（P2P 设备互联 + HTTP API 共用） |
| `device_name` | 本机在对端显示的名称（默认取主机名） |
| `hotkeys.switch_right` | 切换到右侧设备的快捷键（helper 实现后生效） |
| `hotkeys.switch_left` | 切换到左侧设备的快捷键（helper 实现后生效） |

---

## 当前功能范围

| 功能 | 状态 |
|------|------|
| 守护进程 + Unix Socket IPC | ✅ |
| 单端口 TCP（P2P + HTTP API 多路复用） | ✅ |
| P2P JSON Lines 协议 | ✅ |
| 配对：PIN 码 + 接受/拒绝 | ✅ |
| 可信设备持久化（免 PIN 自动接受） | ✅ |
| 多设备同时连接 | ✅ |
| 运行时追加/断开连接 | ✅ |
| 本地多进程测试（按端口隔离） | ✅ |
| 延迟统计（rolling 20 样本均值） | ✅ |
| HTTP API + SSE 事件流（Gin） | ✅ |
| 浏览器 UI（React SPA） | ✅ |
| 快捷键配置 API | ✅ |
| 鼠标边界切换（事件转发，无注入） | ✅ |
| 真实鼠标/键盘注入 | 🔜 需要 helper |

---

## 开发计划

- **下一步**：`helper` — macOS 系统级进程，CGEventTap 全局快捷键捕获 + `CGEventPost` 鼠标/键盘注入，通过 Unix Socket 接入 daemon。
