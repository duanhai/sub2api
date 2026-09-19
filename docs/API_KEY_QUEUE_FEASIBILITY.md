# 指定 API Key 有界等待

基线：`duanhai/sub2api` 的 `a20755df`（v0.2.8）。仅在 feature 分支实现，默认关闭。

## 配置与行为

在服务的 YAML 配置中按数据库 Key ID 配置，不要填写 API 密钥：

```yaml
gateway:
  api_key_queues:
    "123":
      max_waiting: 3
      timeout_seconds: 20
```

同时在管理后台为这个 Key 设置正数并发上限，例如 2。此时最多 2 个请求占用
执行名额、额外 3 个请求等待；再来的请求在名额仍满时被拒绝。正在执行的请求
释放名额后，等待者自动尝试进入。排队不会提高上游账号能力或并发上限。

- `max_waiting` 范围 1..100，`timeout_seconds` 范围 1..60。
- 默认空配置不排队；Key 并发上限为 0 时仍不限制并发。
- 配置在启动时读取；所有实例必须一致，修改后重启。无数据库迁移、无新增管理界面。
- HTTP 和 Responses WebSocket 首轮及后续轮次经过共同名额申请逻辑。
  Live 仍使用原有组合租约即时准入，不在本功能范围内。
- HTTP 队列满/超时返回 429，错误码为 `api_key_queue_full` / `api_key_queue_timeout`；
  WebSocket 使用原有可重试关闭码 1013。Redis 故障拒绝准入，不绕过限制。
- 使用 200ms 轮询，不保证严格 FIFO，新请求可能先抢到名额。
- HTTP 在 Key 等待期间不发 SSE 保活，以保留正确 HTTP 错误状态。
  等待预算应低于客户端和反向代理的首字节超时；后续用户/账号等待另计。
  客户端重试行为仍由客户端决定，本功能不自动重放失败请求。

## 生命周期边界

Key 等待记录独立于用户/账号名额。Redis ZSET 使用唯一等待 ID、Redis 时间和
逐条到期时间进行原子容量检查；取消/超时移除自己的记录。进程退出或清理失败
时，记录最长在配置等待时长加 5 秒后到期，不会因其他请求持续进入而一直保留。

WebSocket 仅对配置中的 Key 启用单一持续 reader，在业务准入阻塞时继续处理
ping/close。原来的共享读帧函数从这个 reader 取消息，转发器继续按顺序进行
原有协议验证。最多缓存 4 帧、累计不超过现有单帧读取上限，另有一个正在读取的
帧；溢出以策略违规关闭连接，不无限积压。退出时关闭 socket 并等待 reader 结束。

断连信号只取消名额等待；已准入的租约和上游用量回收保持原有生命周期。
等待超时上下文不会成为成功请求的租约上下文，避免等待结束时误释放执行名额。

## 验证

回归覆盖：2 个名额 + 3 个等待者、多实例共享容量、不同 Key 隔离、队列满、
超时/取消清理、HTTP 等待不占用户名额、Redis 故障、WS 排队断连不请求上游、
WS 第二轮等待后恢复、帧顺序/缓存边界/ping/租约丢失关闭码，以及启用 reader
后的 HTTP bridge 和客户端断连后上游用量回收。既有 Key 并发测试继续覆盖默认行为。

本地针对性测试命令（backend 目录）：

```sh
go test -tags=unit -p=2 ./internal/service ./internal/handler ./internal/repository ./internal/config -run 'TestPersistentReader|TestAPIKeyQueue|TestAPIKeyWait|TestLoadAPIKeyQueues|TestReadOpenAIWSClientMessage|TestAPIKeyConcurrency|TestOpenAI.*WebSocket|TestOpenAIWS' -count=1
```

CI 额外运行核心回归的 race 检查，以及仓库原有完整单元、集成和 lint 检查。
本地 Windows 未安装 C 编译器，race 结果以 Linux CI 为准；不把本地通过等同于
生产验证。合并和上线前应确认该提交 CI 通过，并先对单个 Key 启用。

`docs/probes/ws_queue_lifecycle_test.go` 保留为依赖行为探针：它验证升级后的
HTTP request context 不足以可靠感知 WS 断开，因此不能仅加等待循环。
