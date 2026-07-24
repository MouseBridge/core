# CLI 命令参考

## 当前建议

当前稳定可用的 CLI 入口是：

```bash
mousebridge daemon [--data-dir DIR] [--connect IP ...]
```

`core` 的连接、配对、断开、remembered 管理、快捷键管理，当前推荐通过 Web UI 或 HTTP API 完成。

仓库里保留了 `connect` / `pair` / `disconnect` / `trust` 等 Cobra 子命令占位，但它们还没有补齐，不应作为正式运维入口。

## `mousebridge daemon`

前台启动守护进程，监听配置中的 `listen_host:port`，同时提供：

- P2P 连接
- `/api/*` HTTP API
- 本地 helper Unix socket

常用示例：

```bash
mousebridge daemon
mousebridge daemon --data-dir ~/.mousebridge-work
mousebridge daemon --connect 192.168.1.25
```

### 标志

| 标志 | 说明 |
|------|------|
| `--data-dir` | 数据目录，默认 `~/.mousebridge` |
| `--connect` | 启动后立即连接到给定 IP，端口取本机配置中的 `port` |

### 停止

前台运行时直接 `Ctrl+C`。

## 相关入口

- Web UI：见 `ui-web-client/README.md` 与 `ui-web-server/README.md`
- HTTP API：见 [http-api.md](http-api.md)
- Helper 管理：见 `../helper/README.md`
