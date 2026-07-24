# HTTP API 参考

daemon 启动后，在同一个 TCP 端口同时提供 P2P 和 HTTP API，路径前缀为 `/api`。

## 注意事项

- 默认只监听 loopback
- 如果开启 `unsafe_http_lan=true`，HTTP API 会暴露到局域网，且**没有认证**
- `GET /api/status` 与 `GET /api/events` 是对外安全视图：不会返回 inbound pairing 的 `display_pin`
- 需要查看 PIN 时，请使用本机访问的 `/api/local/status` 或 `/api/local/events`
- Web UI 当前就是基于这些接口工作的

## 状态与事件

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/status` | 读取对外安全状态快照（不含 PIN） |
| `GET` | `/api/events` | 对外安全 SSE 事件流，首条消息是安全状态快照 |
| `GET` | `/api/local/status` | 仅本机可访问的管理状态快照（含 PIN） |
| `GET` | `/api/local/events` | 仅本机可访问的管理 SSE 事件流（含 PIN） |

### `GET /api/status`

这是默认对外状态接口。即使 daemon 开启了 `unsafe_http_lan=true`，它也不会返回 `pending_pairs[].display_pin`。

响应示例：

```json
{
  "daemon": {
    "device_id": "0123456789abcdef0123456789abcdef",
    "display_id": "0123456789ab",
    "name": "MacBook-Pro"
  },
  "listening": true,
  "listen_addr": "127.0.0.1:39172",
  "sessions": [],
  "pending_pairs": [],
  "remembered_devices": [],
  "helper_runtime": {
    "executable_found": true,
    "executable_path": "/usr/local/bin/mousebridge-helper",
    "launch_agent_label": "com.mousebridge.helper.abcd1234",
    "launch_agent_plist": "/Users/name/Library/LaunchAgents/com.mousebridge.helper.abcd1234.plist",
    "launch_agent_installed": true,
    "launch_agent_loaded": true,
    "accessibility_granted": true,
    "connected": true,
    "client_count": 1,
    "logs_dir": "/Users/name/.mousebridge/logs",
    "recommended_action": "ready"
  },
  "unsafe_http_lan": false
}
```

`helper_runtime.recommended_action` 当前可能返回：

- `ready`
- `install_helper_binary`
- `install_launch_agent`
- `grant_accessibility`
- `restart_launch_agent`

## 设备连接

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/connect` | `{"host":"192.168.1.6","port":39172}` | 发起到远端 daemon 的连接 |
| `DELETE` | `/api/sessions/:device_id` | — | 断开指定活动会话 |

## 配对

当前服务端在新设备接入时会直接生成 PIN；客户端输入正确 PIN 后即完成配对，不存在单独的“accept pairing”接口。

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/pair/pin` | `{"pairing_id":"...","pin":"847291"}` | 提交 PIN |
| `POST` | `/api/pair/reject` | `{"pairing_id":"..."}` | 取消 / 拒绝当前配对 |

## Remembered 设备

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/remembered` | — | remembered 设备列表 |
| `PATCH` | `/api/remembered/:device_id` | `{"alias":"Desk Mac"}` | 更新别名 |
| `DELETE` | `/api/remembered/:device_id` | — | 删除 remembered 设备 |

## 本机 helper 托管

这些接口主要给 Web UI 的 `Server Console` / `Setup` 页面使用，并且**仅允许本机访问**。

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/local/helper` | — | 读取本机 helper 运行时状态 |
| `POST` | `/api/local/helper/install` | `{}` | 安装或重装 helper LaunchAgent |
| `POST` | `/api/local/helper/restart` | `{}` | 重启 helper |
| `POST` | `/api/local/helper/open-accessibility` | `{}` | 打开 macOS Accessibility 设置页 |

## 本机 validation suite

这些接口主要给 Web UI 的 `Server Console` / `Validation` 页面使用，并且**仅允许本机访问**。

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/local/validation` | — | 读取本机 validation runner 状态、最近摘要与日志摘录 |
| `POST` | `/api/local/validation/run` | 见下方 | 启动 `verify/local-validation-suite.sh` |
| `POST` | `/api/local/validation/stop` | `{}` | 请求停止当前 validation run |

请求体示例：

```json
{
  "text": "MouseBridge local suite",
  "burst_count": 240,
  "burst_batch_size": 40,
  "latency_samples": 12,
  "latency_interval": 0.1
}
```

返回字段包括：

- `available`：当前 daemon 运行环境是否能找到 `verify/local-validation-suite.sh`
- `running`：当前是否有 validation 在跑
- `summary_path` / `log_path`：最近一次运行的摘要文件与日志文件路径
- `summary_excerpt` / `log_excerpt`：最近一次运行的末尾摘录
- `last_request`：最近一次运行的参数
- `last_error` / `last_exit_code`：最近一次运行的退出信息

## 快捷键

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/shortcuts` | — | 当前快捷键配置 |
| `PUT` | `/api/shortcuts` | 见下方 | 更新快捷键并立即推送给 helper |
| `POST` | `/api/control/switch-next` | — | 切到下一个远端设备；没有活动远端时保持在本机 |
| `POST` | `/api/control/switch-prev` | — | 切到上一个远端设备；没有活动远端时保持在本机 |
| `POST` | `/api/control/switch-to-host` | — | 立即切回本机控制 |
| `POST` | `/api/control/disconnect-all` | — | 断开全部会话并停留在本机 |
| `POST` | `/api/control/toggle-pause` | — | 暂停/恢复输入转发 |
| `PUT` | `/api/control/capture` | `{"enabled":false}` | 开启/关闭本机 helper 捕获；关闭后保留注入能力，适合本机双端模拟 |

请求体：

```json
{
  "switch_next": "ctrl+alt+right",
  "switch_prev": "ctrl+alt+left",
  "switch_to_host": "ctrl+alt+escape",
  "disconnect_all": "",
  "toggle_pause": ""
}
```

`GET /api/status` 额外返回：

- `active_target_device_id`：当前控制目标设备 ID
- `controlling_remote`：当前是否处于远端控制态
- `paused`：当前是否暂停输入转发
- `capture_enabled`：当前本机 helper 是否采集本机键鼠事件

## 调试输入接口

这些接口主要用于开发验证，不是最终用户入口。

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/helper/input` | `{"kind":"mouse_move","dx":12,"dy":-4}` | 直接向本机 helper 注入输入 |
| `POST` | `/api/helper/input/batch` | `{"inputs":[...],"step_delay_ms":35}` | 按顺序向本机 helper 注入一组输入 |
| `POST` | `/api/session/input` | `{"device_id":"...","kind":"mouse_move","dx":12,"dy":-4}` | 向指定远端会话发送输入 |
| `POST` | `/api/session/input/batch` | `{"device_id":"...","inputs":[...],"step_delay_ms":35}` | 按顺序向指定远端会话发送一组输入 |

绝对定位示例：

```json
{
  "device_id": "0123456789abcdef0123456789abcdef",
  "kind": "mouse_move_abs",
  "x": 1280,
  "y": 720
}
```

`/api/helper/input` 支持：

- `mouse_move`
- `mouse_move_abs`
- `mouse_button`
- `scroll`
- `key_tap`
- `key_down`
- `key_up`
- `text`

`/api/session/input` 支持：

- `mouse_move`
- `mouse_move_abs`
- `mouse_button`
- `key_down`
- `key_up`
- `key_tap`
- `text`
- `scroll`

## SSE 事件

SSE 中 `event` 固定为 `message`，JSON 内容分两类：

- 首条：完整 `status` 快照
- 后续：`BusEvent`

常见 `kind`：

- `pair_request`
- `pair_retry`
- `pair_reject`
- `pair_timeout`
- `paired`
- `session_connected`
- `session_disconnected`
- `log`
- `error`

如果需要在 CLI / 本机调试时看到 PIN，不要依赖 `/api/status`；请查看 daemon 本地日志，或从本机访问 `/api/local/status` / `/api/local/events`。
