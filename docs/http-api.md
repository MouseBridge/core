# HTTP API 参考

daemon 启动后，在同一个 TCP 端口同时提供 P2P 和 HTTP API，路径前缀为 `/api`。

## 注意事项

- 默认只监听 loopback
- 如果开启 `unsafe_http_lan=true`，HTTP API 会暴露到局域网，且**没有认证**
- Web UI 当前就是基于这些接口工作的

## 状态与事件

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/status` | 读取完整状态快照 |
| `GET` | `/api/events` | SSE 事件流，首条消息是完整状态快照 |

### `GET /api/status`

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
  "unsafe_http_lan": false
}
```

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

## 快捷键

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/shortcuts` | — | 当前快捷键配置 |
| `PUT` | `/api/shortcuts` | 见下方 | 更新快捷键并立即推送给 helper |

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

## 调试输入接口

这些接口主要用于开发验证，不是最终用户入口。

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/helper/input` | `{"kind":"mouse_move","dx":12,"dy":-4}` | 直接向本机 helper 注入输入 |
| `POST` | `/api/session/input` | `{"device_id":"...","kind":"mouse_move","dx":12,"dy":-4}` | 向指定远端会话发送输入 |

`/api/helper/input` 支持：

- `mouse_move`
- `scroll`
- `key_tap`

`/api/session/input` 支持：

- `mouse_move`
- `mouse_button`
- `key_down`
- `key_up`
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
