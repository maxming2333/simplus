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

## 来电不经过桥

`RING` / `+CLIP` 是纯实时主动上报,任何地方都没有存储——那一刻没人接就永久消失。桥固件在监听并走自己的推送管道;Simplus 侧没有生产级通话能力(`CellularVoice` 与 `DigitalVoiceMedia` 在 inventory 映射里是硬编码 false,`application/calls` 只在 Simulator backend 装配),因此不接收来电。

### 来电事件端点

```http
GET {base}/events/calls?after=<seq>&limit=<n>
  -> 200 {"latestSequence":12,"dropped":0,"uptimeMs":39460,
          "events":[{"sequence":12,"number":"+861...","observedAt":1756800000,"observedMs":38210}]}
```

这不在 `/at/*` 命名空间下,因为它不是 AT 中继——桥自己缓冲了本来会消失的事件。

| 字段 | 含义 |
| --- | --- |
| `bootId` | 本次启动的随机标识。**消费方必须先比对它** |
| `sequence` | 单调递增,**重启归零**。消费方的游标 |
| `latestSequence` | 最后一条事件的序号,也是已产生总数 |
| `oldestSequence` | 仍保留在缓冲里的最旧序号;缓冲为空时 0 |
| `observedAt` | 墙钟秒;**为 0 表示记录时未同步**,消费方须回落到自己的接收时刻,不可当成 1970 年 |
| `observedMs` | 记录时刻的设备运行毫秒,用于计算相对时间差 |

**`bootId` 变化时消费方必须把游标重置为 0。** `sequence` 在桥重启后从 1 重新开始,消费方若继续用旧游标(比如 12)就会永久读不到任何事件——而且端点响应正常、两侧日志正常,唯一症状是「来电通知莫名不再出现」,属于最难排查的一类故障。

重启前尚未读走的事件随桥的 RAM 一起消失,无法补取。重置是唯一正确动作;`bootId` 的作用是把这件事**显式告知**消费方,而不是让它从「读不到东西」去反推。桥可以额外提供 `uptimeMs` 作为辅助信号,但判据必须是 `bootId`——运行毫秒会回绕,而 `bootId` 不会。

若桥新增其他 `/events/*` 端点,必须复用同一个 `bootId`:消费方需要能用一个值判断「是否同一次启动」。

### 丢失量由消费方推导,桥不做统计

```
lost = max(0, oldestSequence - (lastSequence + 1))
```

桥**不能**自己数「覆盖了多少条」:它不知道消费方读到哪了(游标是消费方的状态,而且原则上可以有多个读者)。那种计数在正常运行下必然虚增——缓冲满之后每次覆盖都累加,即便那条早已被读走,于是攒够容量次来电就开始报假的丢失。

桥只报告「我还留着的最旧序号」,每个消费方用自己的游标相减,结果对每个读者都精确。这与带游标的日志(如 Kafka 的 log-start-offset)是同一个契约。

参考固件缓冲 32 条,满时丢最旧——来电是瞬时事件,保留最近的比保留最早的有用。

桥必须把记录点放在它自己推送通知的同一个汇聚点上,这样事件与推送严格同源:不会出现「推送了但外部系统查不到」或反之。

**不要把来电伪装成短信塞进存储列举的结果里。** 那需要桥伪造合法的 SMS-DELIVER PDU、分配不与模组冲突的假存储索引、并连带拦截该索引的读取与删除;伪造索引还会与 `PDUDigest`(防索引复用误删)正面冲突。更根本的是它让 transport 变成数据源,上层会收到没有任何模组产生过的数据,排查时必然把人带错方向。

若将来确实要让 Simplus 看到来电,正确做法是建模成独立的通话事件能力(agentapi 上一条有界读操作 + 领域事件),并先立决策记录。

桥固件必须保证:提示符交互期间独占串口时,到达的主动上报要转交给自己的 URC 路由而不是当作响应字节吞掉。否则每条出站短信都会造成最长 30 秒的事件丢失窗口。

## 短信消费归属：胖模式 / 瘦模式

一条入站蜂窝短信只能有一个消费者。桥固件用一个开机模式表达这件事：

| 模式 | 模组设置 | 行为 |
| --- | --- | --- |
| 胖模式（默认） | `AT+CNMI=2,2` | 新短信直投桥固件成为主动上报，**不进模组存储**，由桥固件自己解析并推送 |
| 瘦模式 | `AT+CNMI=2,1` | 新短信落入模组/SIM 存储并只给一条入库指示，桥固件不消费，由 Simplus 轮询取走并删除 |

**Simplus 要收蜂窝短信必须开瘦模式。** 胖模式下 `AT+CMGL` 永远返回空——不是因为没短信，而是因为短信根本没进存储。这是最容易误判为「Simplus 坏了」的情形。

两种模式互斥，不能都要：同时消费会产生「有时进桥固件推送、有时进 Simplus」这种最难排查的状态。

瘦模式下如果 Simplus 没在取，短信会堆积在存储里（容量通常 30 条），满了之后新短信会被网络重投或丢弃。这是 store-and-poll 的固有代价，Simplus 在 ack 之后才删除。

参考固件把这个开关放在管理页面，保存后重启生效。

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
| `commandTimeoutMs` | 1000..180000，默认 20000。**普通命令的上限，Agent 会把调用方的超时钳到这个值** |
| `exchangeTimeoutMs` | 1000..180000，默认 30000。提示符类命令的上限，同上 |
| `responseSizeBytes` | 8192..65536,默认 8192。**响应上限**,见下节 |
| `headers` | 最多 8 条自定义请求头，供使用非 HTTP Basic 认证的桥。拒绝改写请求形态的头（`Content-Type`/`Content-Length`/`Host`/`Accept`）与 hop-by-hop 头，值不得含控制字符 |
| `attestCapabilities` | 见下节。默认 `false` |

### 为什么响应上限也必须可配

默认 8192 字节对探测和 APDU 透传是合适的(与本机 tty 一致),但**装不下满载的短信存储列举**。

算术:一条 160 字符 GSM7 短信的 TPDU 约 152 字节 → 304 个 hex 字符,加 SMSC 与 `+CMGL: n,s,,len` 头行约 **340 字节/条**;SIM 的 `EF_SMS` 常见 40 槽,满载约 **13.6 KB**。8192 只能装约 24 条。

而截断**不是部分结果**:最后一行不完整、拿不到终止状态,整批被判非法。于是存储既读不出来也排空不掉,只会越攒越死——这是攒到一定数量才触发的故障,比其他情形更晚暴露也更难联想。

桥固件侧有对应的上限(参考固件把 AT 响应缓冲从 640 提到 24 KB,并且只对需要的命令按需分配,避免队列里每个槽都占大内存)。两侧都要够,只抬一侧无效。

### 为什么超时上限必须可配且默认很小

调用方的短信派发预算是按**本机直连模组**选的（Simplus 是 120s）。MCU 级别的桥根本没法按那个时长占用自己：参考固件的两个 handler 运行在 async_tcp 单任务上，等待期间**整个 HTTP 服务器停摆**。实测按 120s 阻塞时，该请求本身超时，且紧随其后的请求全部失败——一次回环测试 5 次里 4 次因此失败。

所以两侧都要有上限，而且是两个独立的闸：

- **Agent 侧**（`commandTimeoutMs` / `exchangeTimeoutMs`）：钳住「要求桥等多久」。这是给运维调的旋钮
- **桥固件侧**：无论调用方要多久，桥只占用自己能承受的时长，超出即 `504`。参考固件是普通命令 20s、提示符命令 30s（后者与其原生短信路径一致）

`504` 的语义是「已发出但结果未知」，对端不得重试。所以小上限是安全的：它把不确定性显式化，而不是把设备拖死。代价是慢网络下的提交会被报为 unconfirmed——**实测确认过若干这类「结果未知」的提交其实已经投递成功**。要减少这种情况就得同时抬高两侧的上限，并接受设备在此期间不响应。

文件必须是常规文件、非符号链接、模式不含 group/other 位（例如 `0600`），大小不超过 64 KiB。任一条不满足即启动失败。缺少该配置时 Agent 行为与不带此功能完全一致。

### 为什么不在网页上填

USB 路径**没有任何设备级配置**——内核枚举出设备，插上即出现。网页上为 USB 设备配置的是
业务意图（从候选「添加模组」、从 SIM/Profile 候选「添加 Line」，只填显示名），桥在这一层
与它完全一致。差异只在「设备存在」这一层，而桥没有总线可枚举，只能由人断言，这属于硬件
事实而非业务意图。

把地址与凭据放进 Web 会让网页调用方间接决定 AT 命令的去向，破坏 `attransport.Query` 只对
编译进来的型号 adapter 可见这条不变量；`simplusd` 也按容器隔离契约本就写不到 Agent 的私有
状态。完整理由与将来若要开放网页写入的前置条件见
[`decisions/0028`](decisions/0028-remote-at-bridge-configuration-ownership.md)。

只读可见性由已有渠道提供，不新增 Web 面：

```bash
# Agent 启动日志：每个桥一条 key/profile/host，明文与背书各一条告警
docker compose logs agent | grep -i bridge

# 桥设备出现在清单里；VID:PID 为空本身就是「非 USB 控制路径」的诚实信号
simplusctl health agent
# - China Mobile IoT ML307A (esp32-a :): complete
```

改一个桥 = 改文件 + `docker compose ... restart agent`。

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

## 一次操作 = 一个会话

Simplus 侧每个短信操作（列举 / 读取 / 删除 / 提交）只开**一个**桥会话：

```
POST /at/session
POST /at/command   AT+CMGF=0
POST /at/command   AT+CPMS="SM","SM","SM"
POST /at/command   AT+CMGL=4          ← 或 CMGR / CMGD，多段提交则是多次 /at/exchange
DELETE /at/session
```

模式与存储选择每次操作都重新下发（它们是模组粘性状态，但模组复位会静默丢掉，而桥接路径看不到 USB 重新枚举，因此不能缓存）。把它们和操作本身放进同一个会话有两个作用：往返从 9 次降到 5 次，更重要的是**独占窗口是「每操作」而不是「每命令」**——否则别的消费者能插在 `AT+CMGF=0` 与 `AT+CMGL=4` 之间，多段短信也可能被别人的命令切开。

桥固件因此需要能承受一个会话内连续多条命令，并且在会话未关闭前拒绝其他调用方（这已经是 `409` 的语义）。

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
