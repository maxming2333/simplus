# 远程 AT 桥接契约

本文定义 `simplus-agent` 与「远程 AT 桥」之间的固定 HTTP 契约，供桥固件作者实现。

远程 AT 桥是一台独占持有某个模组 AT 串口的小设备（例如接在 ML307A 上的 MCU），它把这条串口以认证 HTTP 暴露出来。`simplus-agent` 因此可以驱动一枚并未插在本机的受支持模组。

这不是通用的 AT 网关，也不是公开 API。`attransport.Query` 仍然只对编译进来的型号 adapter 可见：Web、公共 API 与应用层都无法经由任何路径下发 AT 命令。桥只承担「把已经选定的一条有界命令送到串口，并把有界响应行送回」这一件事。

## 为什么是 HTTP 而不是 MQTT

- `attransport.Session.Query` 本身就是请求/响应语义，HTTP 一对一映射，MQTT 需要自建关联 ID、有序性与超时；
- 这条 transport 不消费 URC。本地 tty 实现在每次写入前 flush 输入缓冲，桥必须保持同样语义；
- SIM AKA 是「打开逻辑通道 → 交换 APDU → 关闭通道」的粘性序列，必须落在同一条串口会话上。HTTP 用服务端下发的 session token 直接表达这件事；
- HTTP 方案不需要额外的 broker 容器。

## 三个操作

基址由 Agent 私有配置给出，只含 scheme、host 和可选路径。可选 HTTP Basic 认证。桥固件必须保证同一时刻只有一个 session 持有串口。

### 打开会话

```http
POST {base}/at/session
```

| 响应 | 含义 | Agent 分类 |
| --- | --- | --- |
| `200` + `{"session":"<token>","expiresInMs":30000}` | 已独占串口 | 成功 |
| `409` / `423` / `429` | 另一个会话正在持有串口 | busy，可重试 |
| `401` / `403` | 认证或授权失败 | permission，不重试 |
| `400` / `422` | 桥拒绝该请求形态 | configure，可重试 |
| `404` / `405` / `501` | 桥未实现本契约 | unsupported，不重试 |
| 其他 / 连接失败 / 超时 | 桥暂时不可用 | unavailable，可重试 |

`session` 必须匹配 `^[A-Za-z0-9._~-]{1,128}$`。Agent 会拒绝空 token、含空格的 token 以及带未知字段的响应体。

### 下发一条命令

```http
POST {base}/at/command
{"session":"<token>","command":"<单行命令，无 CR/LF>","timeoutMs":1500}
```

```http
200 {"lines":["<响应行>", "...", "OK"]}
```

契约要点：

- `command` 长度 1..1024 字节，且不含 CR/LF。Agent 在发出请求前就会拒绝越界命令，桥应做同样校验。桥可以有更小的上限（参考固件受串口命令缓冲限制为 575 字节，足够容纳最长的 APDU 透传命令），但**必须拒绝而不是截断**：截断后的 AT 命令可能正好是另一条合法命令；
- 桥负责补 `\r`、读取到终止状态、按行拆分。终止状态是 `OK`、`ERROR`、`+CME ERROR:` 前缀或 `+CMS ERROR:` 前缀之一；
- `timeoutMs` 是桥等待终止状态的预算。超时应返回 `504`，不要返回一个没有终止状态的 `200`：Agent 会把缺少终止状态的 `200` 判为失败；
- token 不匹配当前会话时返回 `404` 或 `410`，不要服务该命令。这正是 SIM AKA 依赖的隔离；
- 响应体总长上限 8192 字节，行数上限 256。Agent 会拒绝超限响应；
- 回显、空行和控制字符由 Agent 再规范化一次，但桥不应刻意依赖这一点。

### 提示符类命令

```http
POST {base}/at/exchange
{"session":"<token>","command":"AT+CMGS=23","payloadHex":"<载荷字节的十六进制>","timeoutMs":15000}
```

3GPP TS 27.005 里有一类命令不是请求/响应：`AT+CMGS`（以及 `AT+CMGW`）先回一个 `>` 提示符，调用方再写入 PDU 载荷。桥必须把这三步做成一个原子操作：写命令 → 等 `>` → 写载荷 → 读到终止状态。

载荷用十六进制传输，因为它是二进制的——3GPP 提交载荷以控制字符 `0x1A` 结尾，无法直接放进 JSON。桥解码后写入原始字节。

| 响应 | 含义 | Agent 处理 |
| --- | --- | --- |
| `200` + `{"lines":[...]}` | 拿到终止状态 | 正常返回 |
| `412` | **确定载荷未发出**（没等到提示符、模组在提示符前返回错误、串口拿不到） | 判定操作无副作用，可安全重试 |
| `504` | 载荷已写出但没等到终止状态 | 结果不确定，**不得重试** |
| `410` | token 陈旧 | 与 `/at/command` 一致 |

`412` 与 `504` 的区分不是锦上添花：混为一谈会导致短信重复发送。桥必须只在**确知载荷未写入串口**时返回 `412`。

### 关闭会话

```http
DELETE {base}/at/session
{"session":"<token>"}
```

返回 `200` 或 `204`。Agent 侧的关闭是 best-effort：桥必须能在 `expiresInMs` 之后自行回收会话，否则一次丢包就会永久占住串口。

## 配置文件

Agent 通过 `-remote-at-config` 或 `SIMPLUS_AGENT_REMOTE_AT_CONFIG` 读取一个私有 JSON 文件。凭据不走命令行参数，因为本机任何进程都能通过 `/proc` 看到命令行。

```json
{
  "bridges": [
    {
      "key": "esp32-a",
      "baseUrl": "http://bridge.lan",
      "profile": "ml307a",
      "username": "agent",
      "password": "<bridge credential>",
      "requestTimeoutMs": 20000,
      "attestCapabilities": false
    }
  ]
}
```

| 字段 | 约束 |
| --- | --- |
| `key` | `^[a-z0-9][a-z0-9-]{0,30}$`，全局唯一，构成设备 ID `bridge-<key>` |
| `baseUrl` | 只允许 `http` / `https`，必须有 host，不允许 userinfo、query、fragment |
| `profile` | 必须能在 adapter registry 中解析；拥有专用 driver transport 的型号（例如 QDC507 SMS）会被拒绝 |
| `username` / `password` | 同时给出或同时省略；用户名不含冒号 |
| `requestTimeoutMs` | 1000..120000，默认 20000 |
| `attestCapabilities` | 见下节。默认 `false` |

文件必须是常规文件、非符号链接、模式不含 group/other 位（例如 `0600`），大小不超过 64 KiB。任一条不满足即启动失败。缺少该配置时 Agent 行为与不带此功能完全一致。

明文 `http` 是允许的（局域网部署是预期形态），但 Agent 会在启动时打印一条告警：AT 流量与桥凭据在传输中不具备保密性。

## 能力证据与显式例外

型号 adapter 声明的 `observed` 证据来自本机直连模组的受控 HIL。这份证据不会自动迁移到第三方桥，因此默认策略是：桥设备上所有 `observed` 能力降级为 `unverified`，证据文本写明原因。

后果是 fail closed 且是预期的：

- `internal/application/inventory` 只把 `observed` 映射为业务能力，因此桥设备只会作为物理设备出现，不产生 modem function、SIM 卡槽或 Line；
- `hardwareprobe` 的 SIM 鉴权入口返回 `SIM_AKA_UNSUPPORTED`；
- 探测本身不受影响。运维可以先确认桥可达、SIM READY、注册状态，再决定是否背书。

`"attestCapabilities": true` 保留 adapter 的原始状态，并在每条 `observed` 证据末尾追加「operator-attested remote bridge」。这是运维背书，不是证据；Agent 启动时会为每个被背书的桥打印一条告警。这是 `.trellis/spec/core/backend/application-boundaries.md` 型号隔离规则所要求的「记录明确例外」。

## 参考实现与已验证范围

参考桥固件是 [esp32-sms-forwarding](https://github.com/maxming2333/esp32-sms-forwarding) 的 `/at/session`、`/at/command` 端点。它在会话期间拒绝自身网页调试入口（`GET /at`、`POST /ping`）的 AT 注入，因为那两条路径可以插入任意命令或直接独占串口。

已在真机验证：`/at/exchange` 的两条关键分支——`AT` 带载荷正确返回 `412`（载荷未发出），`AT+CMGW` 的完整提示符交互返回 `200` 与 `+CMGW: <index>`（只写模组存储，不发送，随后已删除清理）。蜂窝短信入站链路 `AT+CMGF=0` / `AT+CPMS="SM","SM","SM"` / `AT+CMGL=4` 经桥由 Simplus 真实 SMS driver 驱动通过。

已在真机验证（读)：一枚 ML307A-DSLN-MTSH1S00 经桥完成完整只读综合探测——`state=complete`、型号/IMEI 指纹、SIM present+ready、SIM 身份指纹与归属 MCC-MNC、CSQ 信号与三域注册状态；以及 `AT+CCHO` → `AT+CGLA` → `AT+CCHC` 的完整粘性逻辑通道序列，关闭后通道号可被重新分配。会话独占、陈旧 token 拒绝、命令越界拒绝、TTL 自动回收均按契约表现。仍未验证的部分见 [`compatibility.md`](compatibility.md)。

可重放这份证据的入口是 `internal/hardwareprobe` 里的 opt-in 探测测试，通过 `SIMPLUS_REMOTE_AT_HIL_CONFIG` 指向私有桥配置启用。它只执行只读计划，不做任何写入、射频变更或 SIM 变更。

## 部署

基础 `compose.yaml` 保持 Agent `network_mode: none`。桥在局域网上，因此必须显式叠加：

```bash
docker compose -f compose.yaml -f containers/compose.remote-at.yaml up -d
```

发布 bundle 中该文件位于 bundle 根目录，命令相应改为 `-f compose.remote-at.yaml`。

叠加文件只做三件事：给 Agent 默认 bridge 网络、挂入一个只读配置文件、设置一个环境变量。它不增加 capability、设备、可写挂载、host 网络或 privileged 模式，`internal/containercontract` 有对应契约测试。

把 Agent 从 `none` 放开到 bridge 网络是一次真实的隔离放宽。建议把桥放在独立管理网段，并只给桥所需的最小可达性。

## 路由不是回退

控制端点定位符决定 transport：带桥前缀的走 HTTP transport，其余走本机 tty transport。选中的 transport 打不开就直接返回错误，绝不改走另一条。静默切换 transport 会让实际控制路径取决于瞬时可达性，这被架构不变量禁止。

定位符只出现在 `agentapi.Endpoint.Node`——本机模组在同一字段里放 `/dev/ttyUSB2`。它不携带 host、端口或凭据；这些只存在于 Agent 私有配置里。`internal/atremote` 有一个源码扫描测试，确保除 transport 与其装配点之外没有任何包引用这个前缀，也就是说上层无法区分本机与桥接。
