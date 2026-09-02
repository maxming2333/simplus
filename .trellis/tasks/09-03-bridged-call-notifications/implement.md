# 桥接来电通知实施计划

## 1. 执行前置与约束

- 桥固件侧已完成并过真机:`GET /events/calls`、`bootId`、`oldestSequence`,`+CLIP` URC 已记录。本计划只做 Simplus 侧消费。
- 这是一个原子跨层交付。agentapi 操作、atremote 客户端、calls schema 与应用层游标必须同版本落地,不拆成可独立发布的子任务。
- 不新增第二个配置入口。桥凭据必须继续只走 `atremote.LoadConfig` 的私有文件(见 ADR 0028);轮询要复用 `atTransportPlan` 已持有的同一份 `atremote.Config` 与共享 `*http.Client`。
- 不改动既有出站/Simulator 通话状态机,不动 `Dial/Answer/Reject/Hangup` 与 `transition`。
- application 层不注入 logger。两条 warning 必须以 report 字段的形式上浮到 `cmd/simplusd` 回调里再打印,与 SMS 同步同构。
- 路由事实(`at-bridge:` scheme)不得泄漏到 `agentapi`。分支只允许发生在 `atremote` 内部或装配 transport 的可执行文件里(`internal/atremote/locator.go` 的硬性约束)。
- 真实来电 HIL 保持 opt-in,不进默认验证阶梯。

## 2. 契约(已从固件源码确认,不是推测)

`GET /events/calls?after=<seq>&limit=<n>`,`CallEvents::CAPACITY = 32`:

```json
{
  "bootId": "0f3a1c2b4d5e6f70",
  "latestSequence": 7,
  "oldestSequence": 1,
  "uptimeMs": 913442,
  "events": [
    {"sequence": 7, "number": "15817320262", "observedAt": 1772505600, "observedMs": 913001}
  ]
}
```

- `bootId` 固定 16 位十六进制。
- `observedAt == 0` 表示记录时墙钟未同步,消费方必须回落到自己的接收时刻,**不得**当成 1970 年。
- `number` 上限 23 字符,可能是占位文本而非号码,因此不能假定它一定通得过号码校验。

## 3. WP0 — 激活与基线

- [ ] `python3 ./.trellis/scripts/task.py start`,确认状态 `in_progress`。
- [ ] 记录 `git status --short`,确认工作区只含本任务改动。
- [ ] 跑一遍基线,确认失败名单与已知的 darwin/GNU-find 环境问题一致,不是新引入的。

验证:

```bash
make check-format && GOOS=linux go vet ./cmd/... ./internal/...
go test ./internal/... ./cmd/...
```

回滚点:仅任务状态。

## 4. WP1 — atremote 事件客户端

所有权:`internal/atremote/events.go` 及其测试;`internal/atremote/opener.go` 的路径常量。

- [ ] 加 `eventsCallsPath` 到既有 path 常量组,与 `sessionPath/commandPath/exchangePath` 并列。
- [ ] 新增 `CallEventsSnapshot` / `CallEvent` 类型,字段对齐上节契约。
- [ ] 在 `*Opener` 上加 `CallEvents(ctx, endpoint string, after uint32) (CallEventsSnapshot, error)`:`ParseLocator` 取 key → 查 target 表 → 用无 token 的 `session` 值调 `do` 发 GET。
  - 复用 `session.do` 而不是新建 client:它已经把 baseURL 拼接、配置头先于 Basic auth、响应上限、凭据擦除都做对了,重写必然漏一项。
  - `do` 在 `payload == nil` 时必须传 nil body,不能给 GET 挂一个 0 长度 reader。
- [ ] 解码沿用包内既有姿势:`DisallowUnknownFields` + 拒绝多对象。
- [ ] 校验 `bootId` 必须是 16 位十六进制;`len(events)` 超过 `32` 直接拒。二者失败都不产生快照。
- [ ] 超时自己用 `context.WithTimeout` 收紧。`Opener.client.Timeout` 是 ~205s 的兜底,轮询不能依赖它。
- [ ] **不占用 AT 会话**:这是无会话 GET,不进 `OperationGate`,不与 modem UART 互斥。

验证:`go test ./internal/atremote`

回滚点:新文件加一个常量,无既有行为变化。

## 5. WP2 — agentapi 有界读操作

所有权:`internal/agentapi/call_events.go`、`call_events_server.go`、`server.go` 的注册与 features、`client.go` 的客户端方法。

- [ ] `FeatureCallEvents = "call-events-v1"`,请求/响应类型,`CallEventsBackend` 接口,sentinel errors。
- [ ] 请求带 `agentInstanceId`/`deviceId`/`deviceGeneration`/`after`,沿用 `validateSMSRuntimeTarget` 的陈旧性校验形状。
- [ ] handler `POST /v1/calls/events`:有界解码(8<<10)→ service → `classifyCallEventsError` → `writeJSON`。
- [ ] 服务端返回前校验自己 backend 的输出,客户端收到后再校验一遍(双向不信任,与 SMS 一致)。
- [ ] 两处能力开关必须同步:`newHandler` 的 `if != nil` 注册,与 `GET /v1/hello` 的 `features` append。漏后者 app 侧发现不了。
- [ ] `NewManagedHardwareHandler` 的注释显式写了「no route for arbitrary commands, paths, calls」。本次新增的是**只读事件读取**,不是 call mutation;必须更新该注释把边界说清,而不是让注释与代码不符。

验证:`go test ./internal/agentapi`

回滚点:新路由未被任何调用方使用前可直接摘掉注册。

## 6. WP3 — hardwareprobe backend

所有权:`internal/hardwareprobe` 新增 events backend。

- [ ] backend 接收 snapshot + deviceID,自己在 `snapshot.Devices` 里定位 `DeviceReport`,取其 primary AT endpoint 的 `Node`,交给 `atremote.ParseLocator`。`agentapi` 只透传不透明字符串。
- [ ] **不要**把轮询结果塞进 `DeviceReport`。`ExtraDevices` 必须保持确定性,内容抖动会 churn snapshot revision 并作废排队中的操作。

验证:`go test ./internal/hardwareprobe`

## 7. WP4 — calls 数据集 v4:游标与已观测来电

所有词:`internal/storage/sqlite/migrations/calls/00004_*.sql`、`internal/storage/sqlite/calls.go`、`store_test.go` 版本锚点。

- [ ] 新增 `call_event_cursors(device_id PK, boot_id, last_sequence, updated_at_unix_ms)`,`WITHOUT ROWID`,每列带 `CHECK` 长度/范围约束,Up/Down 双向并把 `schema_version` 改到 4 / 回 3。
- [ ] 同步 `store_test.go` 里 `{CallsDataset, "migrations/calls", …, 2}` 的版本锚点,否则 down-migration 测试会失效。
- [ ] 新增 `RecordObservedInboundCall`,**不复用 `CreateCall`**:后者刻意把 `end_reason` 写死为 `''` 且不写 `answered_at/ended_at`,因为它只负责「开始一通电话」。已观测来电是一次性写入的终态记录,需要自己的语句。
- [ ] 记录形态:`Direction=inbound`、`State=ended`、`EndReason=CALL_NOT_ANSWERED`、`AnsweredAt=NULL`(确实从未接听)、`EndedAt=观测时刻`。
  - 用 `ended` 而不是新增状态:新状态要改 CHECK 约束、openapi enum 与前端,而 `ended` 加一个专用 reason 已经表达清楚,且与既有 `CALL_INTERRUPTED_BY_RESTART` 同一个模式。
  - `EndedAt` 取观测时刻是为了保住表的 `state='ended' ⇒ ended_at IS NOT NULL` 不变式(`SetCallState` 建立的);桥只报到达,我们从不接听,所以这是一个点事件。这一取舍要写进方法注释。
- [ ] 绝不能让它变成活动通话:`State=incoming` 会被 `HasActiveCallForLine` 当成占线,使第二通来电被 `replayOrBusy` 以 `ErrLineBusy` 拒掉,还会被 `ReconcileCalls` 在下次重启时刷成 `failed`。

验证:`go test ./internal/storage/sqlite`

回滚点:migration 有 Down;应用层未接入前新表是惰性的。

## 8. WP5 — 应用层游标同步

所有权:`internal/application/calls` 新增入站同步文件及测试。

- [ ] 稳定身份 `(deviceID, bootId, sequence)` 摘要,沿用 `inboundSourceID` 的前缀+SHA-256 形状。必须含 `bootId`,否则新一次启动的 sequence 1 会和上一次的 sequence 1 撞。
- [ ] 游标状态机严格按 design.md:`bootId` 变化 → 游标归零 + 一条 warning;事件按 sequence 升序、`> lastSequence` 才处理;**先持久化再推进游标**。
- [ ] 丢失量由消费方推导:`lost = max(0, oldestSequence - (lastSequence + 1))`。桥不数覆盖次数——它不知道消费方读到哪、也可能有多个读者,那种计数环一满就虚增。
- [ ] Line 解析不出来时:不产生记录,但**照样推进游标**,否则一个无法归属的事件会永久堵住它后面的所有事件。与「无法解码的短信降级而不是让整批失败」同一个判断。
- [ ] `observedAt == 0` 回落到接收时刻,绝不让 1970 进持久层。
- [ ] `number` 通不过校验时按 Line 解析不出来同样处理:降级、不建记录、推进游标。
- [ ] report 结构体上浮 `BridgeRestarted bool` / `LostEvents int` / 计数,不注入 logger。
- [ ] 丢失量**只产生一条 warning**,永不产生通话记录:一条没有号码没有时间的记录与真实漏接无法区分,会污染这个功能本身要产出的数据。

验证:`go test ./internal/application/calls`

## 9. WP6 — 装配与可观测

所有权:`cmd/simplus-agent/remoteat.go`、`cmd/simplus-agent/main.go`、`cmd/simplusd/main.go`。

- [ ] `planATTransport` 额外保留具体类型的 `*atremote.Opener`(现在被 `NewRoutingOpener` 包起来后类型丢了)。
- [ ] agent 侧装配 events service 并传给 handler。
- [ ] simplusd 侧复用既有 2 秒 `SyncCoordinator` 节奏,不新开 timer:最小间隔已经限住了负载,入站短信与入站来电是同一个「轮询、持久化、通知、发布」形状。
- [ ] 回调里打两条 warning,**只带计数,不带主叫号码**(design.md 的隐私要求)。

验证:`GOOS=linux go build ./...` + 全量 `go test`

## 10. WP7 — 文档与验证收口

- [ ] `docs/remote-at-bridge.md` 补消费方行为:游标、bootId 重置、丢失量推导。
- [ ] `docs/compatibility.md` 记录来电通知的证据等级。
- [ ] `make check-docs`(禁 RFC1918,用 192.0.2.x)。
- [ ] opt-in HIL:真实来电落成一条 Line 记录,需要桥可达且有人拨号。

## 11. 测试清单(对齐 design.md)

- agentapi:畸形 envelope、缺 `bootId`、超量事件列表,两端都测。
- 游标:重启重放、重复 sequence、持久化失败、乱序事件、跨两次启动的 sequence 1 冲突。
- 丢失量:每次推进只一条 warning、永不建记录、已被读空的环推导出 0 所以忙线不刷日志。
- 时钟:`observedAt` 为 0 时回落,持久层不出现 1970。
- 隐私:主叫号码不出现在普通日志里,重启与丢失两条 warning 只带计数。
