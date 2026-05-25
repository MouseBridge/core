# MouseBridge

在局域网内的两台 Mac 之间共享鼠标和键盘操作。

> **当前状态：Step 1 — 通信层**
> 已实现守护进程架构 + 设备配对 + 事件转发 + 延迟日志。
> 尚未实现真实的输入注入（Step 3），鼠标/键盘事件目前仅做记录和转发。

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

每台机器运行一个后台守护进程（`mousebridge daemon`），所有状态由守护进程持有。

CLI 命令通过 Unix Socket 向守护进程发送指令并接收事件流：

```
mousebridge daemon          ← 守护进程（长驻）
    │
    ├── Unix Socket (~/.mousebridge/mb.sock)
    │       ↑
    │   mousebridge serve / connect / status / pair
    │   （薄客户端，发送命令、打印事件）
    │
    └── TCP :39172
            ↕
    对端 mousebridge daemon（另一台机器）
```

---

## 快速开始

### 两台机器互联

**机器 A（从机，接收控制）：**

```bash
# 终端 1：启动守护进程
./mousebridge daemon

# 终端 2：开始监听 TCP 连接
./mousebridge serve
```

**机器 B（主机，发送控制）：**

```bash
# 终端 1：启动守护进程
./mousebridge daemon

# 终端 2：连接到机器 A
./mousebridge connect <机器A的IP>
```

连接建立后，机器 B 会显示：
```
[pair] request from MachineA — PIN: 847291
[pair] run: mousebridge pair accept  OR  mousebridge pair reject
```

机器 A 同时也会看到相同的配对请求。

**在机器 A 上接受配对：**

```bash
# 新开一个终端
./mousebridge pair accept
```

配对完成后双方均显示：
```
[pair] paired with MachineA
[conn] connected to MachineA (192.168.1.x:39172)
```

---

### 单机测试（两个守护进程）

本地测试时可以用 `--socket` 指定不同的 socket 路径，让两个守护进程共存：

```bash
# 守护进程 A（从机角色，端口 39172）
./mousebridge daemon --socket /tmp/mb-a.sock --port 39172

# 守护进程 B（主机角色，端口 39173）
./mousebridge daemon --socket /tmp/mb-b.sock --port 39173

# 让 A 开始监听
./mousebridge --socket /tmp/mb-a.sock serve

# 让 B 连接 A
./mousebridge --socket /tmp/mb-b.sock connect 127.0.0.1 --port 39172

# 在 A 侧接受配对
./mousebridge --socket /tmp/mb-a.sock pair accept

# 查看连接状态
./mousebridge --socket /tmp/mb-a.sock status
./mousebridge --socket /tmp/mb-b.sock status
```

---

## 命令参考

所有命令都支持 `--socket` 全局标志，用于指定守护进程的 Unix Socket 路径（默认 `~/.mousebridge/mb.sock`）。

### `mousebridge daemon`

启动守护进程。守护进程持有所有 TCP 连接、配对状态和会话数据，其他命令通过 Unix Socket 与它通信。

```bash
mousebridge daemon [--socket <path>] [--port <N>]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--socket` | Unix Socket 路径 | `~/.mousebridge/mb.sock` |
| `--port`, `-p` | TCP 监听端口 | 39172（来自配置文件） |

按 `Ctrl+C` 正常退出，守护进程会关闭所有连接并删除 socket 文件。

---

### `mousebridge serve`

告诉守护进程开始监听 TCP 入站连接（从机角色）。命令保持运行并打印收到的所有事件，直到按 `Ctrl+C`。

```bash
mousebridge serve [--port <N>]
```

---

### `mousebridge connect <ip>`

告诉守护进程连接到指定 IP 上的另一个 MouseBridge 守护进程（主机角色）。命令保持运行并打印所有事件。

```bash
mousebridge connect 192.168.1.5 [--port <N>]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--port`, `-p` | 对端 TCP 端口 | 39172 |

---

### `mousebridge pair`

管理配对请求。

```bash
mousebridge pair accept          # 接受当前待处理的配对请求
mousebridge pair reject          # 拒绝当前待处理的配对请求
mousebridge pair pin <PIN>       # 向对端提交 PIN 码（主机侧提交，跳过对端手动确认）
```

配对有两种完成方式：
- **接受/拒绝**：从机侧运行 `pair accept`
- **PIN 码**：主机侧运行 `pair pin <PIN>`（PIN 由从机显示）

两种方式均可完成配对，哪种先完成即生效。

---

### `mousebridge status`

查询守护进程当前的连接状态。

```bash
mousebridge status
```

输出示例：
```
  MacBook-Pro          192.168.1.5  avg=1.2ms
```

---

## 配置文件

配置文件路径：`~/.mousebridge/config.json`

不存在时自动使用默认值，无需手动创建。

```json
{
  "port": 39172,
  "device_name": "MyMacBook",
  "hotkeys": {
    "switch_right": "ctrl+alt+right",
    "switch_left": "ctrl+alt+left"
  },
  "trusted_devices_file": "~/.mousebridge/trusted.json"
}
```

| 字段 | 说明 |
|------|------|
| `port` | TCP 通信端口 |
| `device_name` | 本机在对端显示的名称（默认取主机名） |
| `hotkeys.switch_right` | 切换到右侧设备的快捷键（Step 3 实现） |
| `hotkeys.switch_left` | 切换到左侧设备的快捷键（Step 3 实现） |

---

## 当前功能范围

| 功能 | 状态 |
|------|------|
| 守护进程 + Unix Socket IPC | ✅ |
| TCP 设备互联（JSON Lines 协议） | ✅ |
| 配对：PIN 码 + 接受/拒绝 | ✅ |
| 多客户端同时监听事件流 | ✅ |
| 鼠标边界切换（事件转发，无注入） | ✅ |
| 延迟统计（rolling 20 样本均值） | ✅ |
| 快捷键切换（终端输入模拟） | ✅ |
| 真实鼠标/键盘注入 | 🔜 Step 3 |
| 图形界面 | 🔜 Step 2 |
| 可信设备持久化 | 🔜 Step 2 |

---

## 开发计划

- **Step 2**：图形 UI，设备管理，布局配置。UI 通过同一个 Unix Socket 接入守护进程，无需修改协议。
- **Step 3**：macOS 真实输入注入（CGo + Accessibility API），替换当前的事件日志占位实现。
