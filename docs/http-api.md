# HTTP API 参考

daemon 启动并开始监听后，HTTP API 与 P2P 流量共用同一 TCP 端口（默认 `39172`），路径前缀 `/api`。

所有请求体和响应体均为 JSON，响应成功时返回 `{"ok": "true"}`，错误时返回 `{"error": "..."}` 及对应 HTTP 状态码。

---

## 设备控制

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET`  | `/api/status` | — | 当前状态快照 |
| `POST` | `/api/serve` | `{"port": 0}` | 开始监听入站连接（`port` 为 0 使用配置默认值） |
| `POST` | `/api/stop-serve` | — | 停止监听 |
| `POST` | `/api/connect` | `{"ip": "192.168.1.6", "port": 39172}` | 连接到远端 daemon |
| `POST` | `/api/disconnect` | `{"device_id": "eb82d0f0"}` | 断开指定设备 |

### `GET /api/status` 响应示例

```json
{
  "serving": true,
  "port": 39172,
  "devices": [
    {
      "id": "eb82d0f0",
      "name": "MacBook-A",
      "ip": "192.168.1.5:54321",
      "avg_latency_ms": 1.2
    }
  ]
}
```

---

## 配对

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `POST` | `/api/pair/accept` | `{"device_id": "eb82d0f0"}` | 接受配对请求（从机侧；`device_id` 可省略，有且仅有一个待处理请求时自动选取） |
| `POST` | `/api/pair/reject` | `{"device_id": "eb82d0f0"}` | 拒绝配对请求（从机侧） |
| `POST` | `/api/pair/pin`    | `{"pin": "847291"}` | 提交 PIN（主机侧） |

---

## 信任设备

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET`    | `/api/trusted` | — | 列出所有已信任设备 |
| `POST`   | `/api/trusted` | `{"device_id": "eb82d0f0", "name": "MacBook-A"}` | 添加信任，后续连接自动免 PIN |
| `DELETE` | `/api/trusted` | `{"device_id": "eb82d0f0"}` | 撤销信任 |

### `GET /api/trusted` 响应示例

```json
[
  {"id": "eb82d0f0", "name": "MacBook-A"}
]
```

---

## 快捷键

| 方法 | 路径 | 请求体 | 说明 |
|------|------|--------|------|
| `GET` | `/api/shortcuts` | — | 读取当前快捷键配置 |
| `PUT` | `/api/shortcuts` | 见下方 | 更新快捷键并推送到 helper |

### 请求 / 响应体结构

```json
{
  "switch_next":      "ctrl+alt+right",
  "switch_prev":      "ctrl+alt+left",
  "switch_to_host":   "ctrl+alt+escape",
  "disconnect_all":   "",
  "toggle_pause":     ""
}
```

---

## 事件流（SSE）

```
GET /api/events
```

长连接，`Content-Type: text/event-stream`。每条事件格式：

```
event: message
data: {"event":"connected","device_id":"eb82d0f0","name":"MacBook-A","ip":"192.168.1.5:54321"}
```

### 事件类型

| `event` 字段 | 触发时机 | 主要字段 |
|-------------|---------|---------|
| `listening` | daemon 开始监听 | `port` |
| `pair_request` | 收到配对请求 | `device_id`, `name`, `pin`（从机侧含 PIN），`role` |
| `paired` | 配对完成 | `device_id`, `name` |
| `connected` | 设备连接建立（session 启动） | `device_id`, `name`, `ip` |
| `disconnected` | 设备断开 | `device_id`, `name` |
| `status` | `GET /api/status` 响应（SSE 通道） | `devices[]` |
| `trusted_list` | `GET /api/trusted` 响应（SSE 通道） | `devices[]` |
| `log` | 普通日志 | `msg` |
| `error` | 错误 | `msg` |
| `stopped` | 监听已停止 | — |
