# 开发说明

## 本地双进程测试

在同一台机器上用两个不同端口模拟两台设备互联。

```bash
# 终端 1 — 进程 A（从机），端口 39172
mousebridge daemon --serve -p 39172

# 终端 2 — 进程 B（主机），端口 39174
mousebridge daemon -p 39174

# 终端 3 — 等两个 daemon 都打印 "socket:" 后再执行

# B 连接 A
mousebridge connect 127.0.0.1 -p 39174 --target-port 39172

# A 日志显示 pair_request 和 PIN
# 在 A 上接受
mousebridge pair accept -p 39172

# 在 B 上提交 A 显示的 PIN
mousebridge pair pin <PIN> -p 39174

# 查看两侧状态
mousebridge status -p 39172
mousebridge status -p 39174
```

### 信任设备（免 PIN 重连）

```bash
# 配对成功后，在 A 上信任 B（device-id 从 status 获取）
mousebridge trust add <B-device-id> --name "MacBook-B" -p 39172

# 断开后重连，A 自动接受，无需 PIN
mousebridge disconnect <B-device-id> -p 39174
mousebridge connect 127.0.0.1 -p 39174 --target-port 39172

# 撤销信任
mousebridge trust remove <B-device-id> -p 39172
```

### HTTP API 验证

A 开启监听后，同一端口同时提供 HTTP API：

```bash
curl http://127.0.0.1:39172/api/status
curl -N http://127.0.0.1:39172/api/events    # SSE 事件流
```

---

## 构建

```bash
go build -o mousebridge ./cmd/mousebridge/
```

## 测试

```bash
go test ./...
```

## 包结构

| 包 | 职责 |
|----|------|
| `cmd/mousebridge/` | 入口，注册 CLI 命令 |
| `internal/cli/` | Cobra 子命令实现 |
| `internal/daemon/` | 守护进程核心，持有所有状态 |
| `internal/transport/` | TCP 多路复用（P2P/HTTP）、连接封装、客户端拨号 |
| `internal/p2p/` | P2P 握手、配对流程、会话管理 |
| `internal/api/` | HTTP 服务（Gin）：REST 路由、SSE hub |
| `internal/session/` | 活跃设备会话状态 |
| `internal/pairing/` | PIN 生成与验证 |
| `internal/switch/` | 边缘检测、停留计时器、切换控制器 |
| `internal/config/` | 配置加载/保存 |
| `internal/event/` | P2P 消息类型与编解码 |
| `internal/trusted/` | 信任设备列表持久化 |
| `internal/shortcuts/` | 快捷键管理，向 helper 推送 |
