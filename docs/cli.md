# CLI 命令参考

所有命令支持 `-p <port>` 指定目标 daemon（通过 socket 路径 `~/.mousebridge/mb-<port>.sock` 匹配），`--socket <path>` 可直接覆盖 socket 路径。

---

## `mousebridge daemon`

启动守护进程，前台阻塞。Ctrl+C 触发优雅关闭：停止监听、断开所有连接、删除 socket 文件。

```bash
mousebridge daemon [-p <port>] [--serve] [--connect <ip> ...]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-p`, `--port` | TCP 端口（P2P + HTTP API 共用），同时决定 socket 路径 | 39172 |
| `--serve` | 启动后立即开始监听入站连接 | — |
| `--connect <ip>` | 启动后立即连接指定 IP，可重复多次 | — |

---

## `mousebridge serve`

告诉 daemon 开始监听 TCP 入站连接（从机角色），发完命令立即退出。

```bash
mousebridge serve [-p <port>]
```

---

## `mousebridge connect <ip>`

告诉 daemon 连接到远端 daemon（主机角色），发完命令立即退出。可多次调用追加连接。

```bash
mousebridge connect <ip> [-p <port>] [--target-port <port>]
```

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--target-port` | 远端 daemon 的 TCP 端口 | 与 `-p` 相同 |

---

## `mousebridge stop-serve`

告诉 daemon 停止监听，已有连接不受影响。

```bash
mousebridge stop-serve [-p <port>]
```

---

## `mousebridge disconnect <device-id>`

告诉 daemon 断开指定设备。device-id 从 `status` 输出获取。

```bash
mousebridge disconnect <device-id> [-p <port>]
```

---

## `mousebridge pair`

管理配对请求。

```bash
mousebridge pair accept [-p <port>]          # 接受当前待处理的配对请求（从机侧）
mousebridge pair reject [-p <port>]          # 拒绝当前待处理的配对请求（从机侧）
mousebridge pair pin <PIN> [-p <port>]       # 向对端提交 PIN 码（主机侧）
```

---

## `mousebridge status`

查看当前连接设备列表及延迟统计。

```bash
mousebridge status [-p <port>]
```

---

## `mousebridge trust`

管理信任设备列表，持久化到 `~/.mousebridge/trusted.json`。

```bash
mousebridge trust add <device-id> [--name <name>] [-p <port>]   # 信任设备，后续连接自动免 PIN
mousebridge trust remove <device-id> [-p <port>]                 # 撤销信任
mousebridge trust list [-p <port>]                               # 列出所有已信任设备
```
