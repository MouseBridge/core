# 开发说明

## 基础构建

```bash
go build -o mousebridge ./cmd/mousebridge/
go test ./...
```

## 本地双端验证（macOS）

仓库根目录有一个集成脚本：

```bash
./verify/local-two-node-macos.sh start
```

它会：

- 启动两个本地 daemon
- 自动执行 `B -> A` 连接
- 在 iTerm 中拉起两个 helper
- 把 B 侧 SSE 输出写到 `/tmp/mousebridge-sse-b.log`

验证重点：

- `ctrl+alt+down`：切到远端
- `ctrl+alt+escape`：紧急返回本机
- 右边缘切换：若 `edge_targets.right` 已配置则切到远端

查看日志：

```bash
./verify/local-two-node-macos.sh logs
```

停止：

```bash
./verify/local-two-node-macos.sh stop
```

## 双真机验证建议

推荐顺序：

1. 两台机器都运行 `mousebridge daemon`
2. 两台机器都打开 Web UI `Setup` 页面，先把 daemon reachability、helper LaunchAgent、Accessibility、helper reconnect 全部做成绿色
3. 确认 daemon 不是 loopback-only；双机时必须让对端能访问该地址
4. 再用 Web UI 发起连接、输入 PIN、确认会话建立
5. 最后验证热键切换、边缘切换、紧急返回键

## 长时间跑与压测工具

仓库内提供两个开发脚本：

```bash
./verify/latency-benchmark.sh http://127.0.0.1:39172
./verify/session-input-burst.sh http://127.0.0.1:39172 <device-id> 2000
./verify/local-session-smoke.sh http://127.0.0.1:39273 http://127.0.0.1:39272 "MouseBridge smoke"
./verify/local-loop-safety.sh http://127.0.0.1:39273 http://127.0.0.1:39272 /tmp/mb-helper-controller.log /tmp/mb-helper-receiver.log "MouseBridge anti-loop"
./verify/local-performance-benchmark.sh http://127.0.0.1:39272 http://127.0.0.1:39273 /tmp/mb-helper-receiver.log 240 40 12 0.1
```

- `latency-benchmark.sh`：周期采样 `/api/status` 中的 `avg_latency_ms`
- `session-input-burst.sh`：通过 `/api/session/input/batch` 分块打突发输入，检查转发链路稳定性和接收端 mouse move coalescing 表现
- `local-session-smoke.sh`：本机双端场景下自动连接/自动配对（若未 remembered），验证期间临时关闭两侧本机捕获，然后发送一段真实的 move + click + text 序列，验证接收端 helper 是否真的把动作注入到 macOS
- `local-loop-safety.sh`：本机双端场景下保持两侧 capture 开启，发送一段真实 remote inject，并检查控制端 helper 日志里没有新的 `sent input kind=` 回流事件，用来验证 anti-loop
- `local-performance-benchmark.sh`：本机双端场景下自动建会话、临时关闭 capture、发送 batch burst、采样 `avg_latency_ms`，并解析 receiver helper 日志里的 injected/coalesced 计数，输出压缩比和延迟摘要

## 日志降噪与调试

默认已经关闭高频输入日志。

如需排查输入链路，可显式打开：

```bash
MB_VERBOSE_INPUT_LOGS=1 mousebridge daemon
MB_HELPER_VERBOSE_INPUT=1 mousebridge-helper run --data-dir ~/.mousebridge
```
