# 配置文件

路径：`~/.mousebridge/config.json`，不存在时自动使用默认值，无需手动创建。

## 示例

```json
{
  "port": 39172,
  "device_name": "MyMacBook",
  "hotkeys": {
    "switch_next":    "ctrl+alt+right",
    "switch_prev":    "ctrl+alt+left",
    "switch_to_host": "",
    "disconnect_all": "",
    "toggle_pause":   ""
  }
}
```

## 字段说明

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `port` | int | `39172` | TCP 端口，P2P 设备互联与 HTTP API 共用，同时决定 Unix Socket 路径（`~/.mousebridge/mb-<port>.sock`） |
| `device_id` | string | 派生自端口 | 本机唯一标识，对端用于识别和信任 |
| `device_name` | string | 主机名 | 本机在对端日志和 UI 中显示的名称 |
| `hotkeys.switch_next` | string | `""` | 切换到下一台设备的快捷键（helper 实现后生效） |
| `hotkeys.switch_prev` | string | `""` | 切换到上一台设备的快捷键 |
| `hotkeys.switch_to_host` | string | `""` | 切换回主机的快捷键 |
| `hotkeys.disconnect_all` | string | `""` | 断开所有连接的快捷键 |
| `hotkeys.toggle_pause` | string | `""` | 暂停/恢复输入转发的快捷键 |

## 信任设备文件

路径：`~/.mousebridge/trusted.json`，由 `mousebridge trust add` 命令自动维护，格式：

```json
[
  {"device_id": "eb82d0f0", "name": "MacBook-A"}
]
```

信任的设备在下次连接时无需 PIN，daemon 自动接受配对。
