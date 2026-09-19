# 指定 API Key 排队：安全边界核查

基线：`duanhai/sub2api` 的 `a20755df`（v0.2.8）。

## 本分支决定

暂不接入生产排队功能。本分支只保存生命周期验证程序和核查结论，
没有改变 HTTP、WebSocket、Redis、配置或数据库行为。
这不是“技术上做不到”，而是不能把当前同步准入直接改成等待循环，
就声称实现了客户端取消安全的 Codex 排队。

## 已确认的接入点

- `backend/internal/service/api_key_concurrency.go` 的 `AcquireAPIKeySlot`：
  当前即时申请 Redis 名额，满额返回 `ErrAPIKeyConcurrencyExceeded`。
- `backend/internal/handler/gateway_helper.go`：HTTP 先拿 Key 名额，再等待
  用户名额。Key 等待者必须独立计数，不能提前占用用户或上游账号名额。
- `backend/internal/handler/openai_gateway_handler.go` 的 `ResponsesWebSocket`：
  接受 WebSocket、读取首个 `response.create` 后才做 Key 准入；之后的轮次
  也在 `BeforeTurn` 中同步准入。
- `backend/internal/service/openai_ws_forwarder_ingress.go` 和
  `openai_ws_v2_passthrough_adapter.go`：存在多个 WebSocket 转发模式，不能
  只改普通 HTTP 或一个 WebSocket 分支。
- Live 使用 `AcquireLiveLease` 原子申请组合名额，没有经过同一个
  `AcquireAPIKeySlot` 调用，未来功能需明确范围，不能宣称所有协议一致。

## 为什么不直接加等待循环

HTTP 请求上下文不能直接当作升级后的 WebSocket 连接生命周期。
如果客户端在等待期间关闭连接，没有活动 reader 的路径不能依赖
`request.Context().Done()` 及时退出；名额释放后，还存在把已经放弃的
请求继续送入后续处理的风险。

所用 `github.com/coder/websocket v1.8.14` 的 `Reader` 负责处理关闭和
ping/pong 帧，同一连接只能有一个活动 Reader。`CloseRead` 虽然返回
断开时取消的上下文，但调用后不能再读业务消息，收到业务消息还会关闭连接。
因此不能通过额外调用 `CloseRead` 或启动第二个 reader 来修补排队。

`docs/probes/ws_queue_lifecycle_test.go` 用本地真实 WebSocket 验证基础边界：
服务器已读取首帧，客户端主动关闭；随后 socket read 报错，但 HTTP 请求
上下文仍未取消。它是依赖行为探针，不是对完整网关排队的端到端测试。

在 `backend` 目录运行：

```sh
go test -count=10 -v ../docs/probes/ws_queue_lifecycle_test.go
```

验证结果：2026-09-20，Windows/amd64、Go 1.27.0，连续 10 次通过，
均观察到 socket 已断开而 HTTP request context 仍处于活动状态。

## 如继续实现，需要先解决

1. WebSocket 单一 reader 与请求处理解耦，所有 ingress 模式统一获得可靠的
   连接取消信号；缓存帧必须有界，并保留协议顺序及现有重试语义。
2. Redis 独立维护每个 Key 的等待记录、容量和期限，使用唯一 request ID
   清理；覆盖多实例、进程崩溃和 Redis 响应不确定的情况。
3. 等待超时上下文和成功准入后的租约上下文分离，避免排队结束的 cancel
   误杀已获得名额的请求。
4. HTTP 首字等待与 SSE 保活的取舍明确：保活一旦写出，之后超时就不能
   再改 HTTP 状态码；需要协议正确的流内错误处理。
5. 明确 Key 等待与后续用户/账号等待的总预算，并在长等待后复核必要的
   鉴权与计费条件。

验收至少包括：2 个名额 + 3 个等待者的容量边界、队列满、等待超时、
客户端断开、不同 Key 隔离、多实例共享、Redis 故障、长响应租约续期、
WebSocket 首轮和后续轮次，以及未开启功能时原有拒绝行为。

在这些前提未得到实现和验证前，不合并排队功能到 main，也不发布新包。
