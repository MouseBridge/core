# CLI 命令参考

## 端口与 Socket

每个 daemon 实例通过端口区分，对应 socket 路径为 `~/.mousebridge/mb-<port>.sock`。

**正常使用（两台机器）**：无需指定端口，默认使用 `config.json` 中的 `port`（默认 `39172`）。

**本地多进程测试**：通过 `MB_PORT` 环境变量指定端口：

```bash
MB_PORT=39172 mousebridge daemon --serve
MB_PORT=39174 mousebridge daemon
MB_PORT=39174 mousebridge connect 127.0.0.1:39172
```

端口解析优先级：`MB_PORT` 环境变量 > `config.json` > 默认值 `39172`。

`--socket <path>` 可直接覆盖 socket 路径，优先级最高。

---

## `mousebridge daemon`

启动守护进程，前台阻塞。Ctrl+C 触发优雅关闭：停止监听、断开所有连接、删除 socket 文件。

```bash
mousebridge daemon [--serve] [--connect <ip> ...]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--serve` | 启动后立即开始监听入站连接 | — |
| `--connect <ip>` | 启动后立即连接指定 IP，可重复多次 | — |

---

## `mousebridge serve`

告诉 daemon 开始监听 TCP 入站连接（从机角色），发完命令立即退出。

```bash
mousebridge serve
```

---

## `mousebridge connect`

告诉 daemon 连接到远端 daemon（主机角色），发完命令立即退出。可多次调用追加连接。

```bash
mousebridge connect <ip>:<port>          # ip:port 一体写法
mousebridge connect <ip> --port <port>   # 分开写法
mousebridge connect <ip>                 # 省略端口，使用远端默认端口
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--port` | 远端 daemon 的 TCP 端口 | 来自 config |

---

## `mousebridge stop-serve`

告诉 daemon 停止监听，已有连接不受影响。

```bash
mousebridge stop-serve
```

---

## `mousebridge disconnect <device-id>`

告诉 daemon 断开指定设备。device-id 从 `status` 输出获取。

```bash
mousebridge disconnect <device-id>
```

---

## `mousebridge pair`

管理配对请求。

```bash
mousebridge pair accept          # 接受当前待处理的配对请求（从机侧）
mousebridge pair reject          # 拒绝当前待处理的配对请求（从机侧）
mousebridge pair pin <PIN>       # 向对端提交 PIN 码（主机侧）
```

---

## `mousebridge status`

查看当前连接设备列表及延迟统计。

```bash
mousebridge status
```

---

## `mousebridge trust`

管理信任设备列表，持久化到 `~/.mousebridge/trusted.json`。

```bash
mousebridge trust add <device-id> [--name <name>]   # 信任设备，后续连接自动免 PIN
mousebridge trust remove <device-id>                 # 撤销信任
mousebridge trust list                               # 列出所有已信任设备
```
