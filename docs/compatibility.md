# 兼容性与验证边界

本文只汇总可公开的兼容性结论。真实部署地址、设备身份、运营商账户、订阅、节点、逐次命令输出和原始日志不进入公开仓库。

## 证据等级

| 等级 | 含义 |
| --- | --- |
| Designed | 只有架构或接口设计，没有可运行实现 |
| Fixture | 使用固定 transcript、fake 或 Simulator 自动化验证 |
| HIL-0 | 在真实硬件上执行固定白名单只读探测 |
| HIL | 在明确授权下完成受控硬件纵切 |
| Runtime | 正式安装形态完成生命周期和恢复验证 |

看到 USB interface、tty、QMI、UAC 或配置字段只属于观察证据，不能自动提升为业务能力。

## 模组

| 型号 | 已验证 | 尚未验证或未开放 |
| --- | --- | --- |
| QDC507 | USB identity、固定 primary AT/QMI 角色、稳定设备/SIM 身份、RF/注册状态；固定参数化 CGSN 模块序列号读取及当前 SIM 的固定只读 subscriber-number 读取有 HIL-0 与边界 Fixture，号码只作为不持久化的 Line 当前观测；PDU-mode GSM7/UCS-2、SIM `SM` 暂存、v2 durable recovery 与 payload-echo 边界有 Fixture；指定 SIM/批准 peer 已完成一条固定 `OK` 入站的应用 persist→PDU revalidate/delete→pending-zero，以及一条新出站的 persist→modem-confirmed HIL；production Agent 和 per-Line native SMS transport 已装配 | 其他 SIM/运营商的号码可用性或准确性、其他收件人的互通、长时间稳定性、真实电话、数字音频、RF 写入 HIL、通用蜂窝数据和 eUICC mutation |
| ML307A | USB identity、固定 primary AT、IMEI/USB Serial 脱敏指纹与 SIM READY/RF Off 白名单探测；SIM 已插入/锁卡/未插入分类 fixture；统一 SIM 鉴权与运行时 RF 控制的 fixture；一张无可用 ISIM 应用的真实 SIM 已完成 IMSI + EF_AD MNC 长度派生 IMS 身份 HIL-0，两位/三位 MNC 另有 fixture；活动 Profile 的 EF_SPN 与归属 MCC-MNC 解析有 fixture，一张可拔插 eUICC 的活动 Profile 已在 HIL-0 中返回可靠归属 MCC-MNC，但未提供可用 EF_SPN；类型化 SIM AKA；Host VoWiFi ePDG/IMS 注册与持续运行；SMS over IMS 真实单段和 multipart 入站；受控单段服务请求的关联出站 RP-ACK、公开 Web/API 异步 `sent` 状态提升、普通号码单段及两段 UCS-2 长短信自号码回环、自动重连后再次收发、multipart 业务回复 | 真实空卡槽 HIL-0、运行时 RF 写入 HIL、上电 RF 策略、RF On 下 Host VoWiFi、其他运营商的 ePDG 发现与接入、SMS over IMS 其他收件人互通、普通蜂窝短信/电话、数字媒体、eUICC mutation、蜂窝数据拨号 |

两个型号共用同一个 Agent 协议、adapter registry、Modem/Line 领域模型和 Web/API；差异只位于经过证据约束的型号 adapter，不包装成两套平台。

## 远程 AT 桥接控制路径

`internal/atremote` 让 Agent 通过 HTTP 驱动一台独占持有模组 AT 串口的远程桥。当前证据等级：

| 项目 | 等级 |
| --- | --- |
| transport 边界（命令/响应长度、终止状态、行规范化、`OpenError` 分类、会话粘性、无回退路由、凭据不入错误文本） | Fixture |
| 桥设备合成、能力降级策略、SIM 鉴权 fail closed | Fixture |
| 配置校验（文件权限、符号链接、URL 形态、profile 解析、重复 key） | Fixture |
| Compose 叠加只放宽网络且不增加特权 | Fixture |
| 经真实桥固件的只读综合探测（型号/IMEI 指纹、SIM present+ready、SIM 身份指纹与归属 MCC-MNC、CSQ、三域注册状态） | HIL-0 |
| 经真实桥固件的逻辑通道粘性序列（通道打开 → APDU 交换 → 关闭，关闭后通道可重新分配） | HIL-0 |
| 提示符交互独占串口期间到达的主动上报被转交而非丢弃 | HIL-0 |
| 桥的来电事件缓冲与有界读取:一次真实来电被记录,主叫号码、墙钟与设备运行毫秒一致,游标语义正确 | HIL-0 |
| 提示符类命令（命令 → `>` → 载荷 → 终止状态）经桥的原子交换，含「载荷未发出」与「结果不确定」的状态分类 | HIL-0 |
| ML307A 蜂窝短信完整回环经桥：出站提交 → 存储投递 → 列举 → PDU 读取 → ack 删除 | HIL（单次通过，重复不稳定） |
| 多条消息共存时的列举与按索引读取/删除 | HIL |
| 每桥可配的命令/提交超时上限与自定义请求头 | Fixture |
| 一次操作复用一个桥会话（列举/读取/删除/多段提交），独占窗口为操作级 | Fixture + HIL-0 |
| 桥固件胖/瘦模式切换真实改变模组上报设置 | HIL-0 |

HIL-0 证据取自一枚 ML307A-DSLN-MTSH1S00 与一台参考 ESP32 桥固件，可通过 `SIMPLUS_REMOTE_AT_HIL_CONFIG` 重放（只读，不含写入/射频/SIM 变更）。

入站短信的消费归属由桥固件的胖/瘦模式决定，二者互斥。胖模式（默认）下新短信直投桥固件、不进模组存储，因此 Simplus 的存储列举永远为空；只有瘦模式才把短信留在存储里交给 Simplus。误判这一点会表现为「Simplus 收不到短信」但两侧日志都正常，细节见 [`remote-at-bridge.md`](remote-at-bridge.md)。

ML307A 蜂窝短信**仍为 `unverified`**。一张已驻网的指定 SIM（电信 LTE，`+CEREG` stat=1）经桥完成过一次完整自号码回环——出站 submit-prompt 提交拿到 `+CMGS` 参考号，入站约 1 秒落入 SIM 存储，按索引读取的解码正文与唯一标记完全一致，ack 后该索引从存储消失且其余待处理消息不受影响。但**重复该回环约 5 次只成功 1 次**，因此不构成可运行能力的证据。

出站延迟分布已实测（6 次自号码回环，电信 LTE）：成功 5 次全部落在 **0.45~0.47 秒**，失败 1 次在 30 秒内**完全没有终止状态**，存储核对确认那条确实没发出去。**分布是双峰的，不是慢尾巴**——要么半秒内答，要么根本不答。

因此抬高超时上限买不到任何成功的发送，只会延长桥的 async_tcp 单任务被占满的时间。两侧的提交超时默认值据此从 30 秒下调到 **10 秒**（实测成功上限的 20 倍，阻塞窗口压到三分之一）；网络条件不同的部署可用每桥的 `exchangeTimeoutMs` 覆盖。

失败样本中既有「已投递但报未知」也有「确实没发出」，因此「结果不确定」是正确分类，应用层不重发是正确行为。失败集中在启动后的首次提交，但样本量不足以断定成因。

已确认的两个成因：
1. 调用方 120s 的短信派发预算超出 MCU 级桥能承受的阻塞时长，桥的 async_tcp 单任务被占满，该请求与紧随其后的请求一并失败。已通过每桥可配的 `commandTimeoutMs` / `exchangeTimeoutMs` 与桥固件自身的硬上限（20s / 30s）缓解，但**上限内 `AT+CMGS` 常拿不到终止状态**，于是被正确地报为「结果未知」——存储检查确认这类提交其实已投递。
2. 模组自身重启发出的 `+MATREADY` 曾被并入在途命令的响应。桥固件已修复识别并在检测到重启后重新下发配置（`CNMI`/`CMGF`/`CGDCONT` 都不写模组 NVM）。

尚未定论：首次提交失败的成因（样本不足）；是否还有第三个成因。要把 `sms-control` 提升为 `observed`，需要该回环可重复通过。

通用 3GPP PDU 模式 driver、durable recovery store 与提交/确认状态机由
`internal/modemadapter/standardsms` 与首个通过蜂窝短信 HIL 的型号共用；共用代码不等于共用证据。桥设备另有独立策略：未经运营者显式背书时，桥上所有 `observed` 一律降级。

无法解码的入站短信不再让整批失败,而是产生一条**降级记录**:可列举、可读取、可 ack,因此存储照常排空,消息也不会静默消失。记录正文只承载可排查的元数据——失败层级(`pdu-structure` / `content-validation` 及其子类)、存储索引、PDU 字节数、DCS、多段位置、以及时间来源是服务中心时间戳还是观测时间——**不含任何未通过校验的内容**。发件人无法解码时用显式占位符而非空串,否则与别处的缺陷无法区分。降级记录的 ID 对同一存储条目 + 同一 PDU 稳定,因此重复列举会被应用层正常去重。

与三方库的对比结论:唯一专门实现 3GPP TS 23.040/23.038 TPDU 的 Go 库 `warthog618/sms` 已 **archived**(75 star,MIT,2024-11 起归档),把解析不可信网络输入的路径交给一个不再修 bug 的依赖,风险方向是反的;`xlab/at` 未归档但是完整 AT 框架,引它等于把串口处理一起拉进来并与本仓库的 transport 层冲突;`fiorix/go-smpp` 一类是 SMPP(运营商侧协议),与模组 TPDU 无关。因此不引入依赖,而是按这些实现补齐了缺口。

对比后确认并修复的两项:未识别的 GSM7 转义序列此前直接报错,现按 TS 23.038 §6.2.1.1 显示为空格——报错既不符合规范,又会让一条本来完整可读的消息因一个未知码降级;国家语言移位表(TS 23.038 Annex A,土耳其语、西班牙语、葡萄牙语及十种印度语言)Simplus 未实现,此前会**忽略** UDH 里的移位元素并用默认表解码,产生看起来合理但错误的文本,现改为显式拒绝并降级——静默错译比失败更糟,读者无法察觉。

来电事件已在真机确认:一次真实来电产生 `sequence=1`,主叫号码为国内格式 11 位(与短信发件人相同的地址类型行为),`observedAt` 与查询时刻相差 21.7 秒且与 `observedMs` 一致,`after` 游标读过即空。`dropped` 的溢出计数尚未验证(需 32 次以上来电)。

Simplus 侧的消费链路尚未实现:领域记录(`DirectionInbound` / `StateIncoming`)与 realtime 的 `TopicCalls` / `AttentionCallIncoming` 都已存在,缺的是 agentapi 上一条有界读操作与轮询该端点的应用层协调,以及游标持久化与 `dropped` 的可观测性设计。

模组自身内存(ME)是否会收到消息:**尚无观测证据**。driver 只选 SIM 存储;按 TS 23.038,class 1(ME-specific)理论上可能被模组存进自身内存,那样 SIM 列举永远看不到、也永远不会被删除。这是从规范做的推断,`AT+CPMS=?` 显示该模组只支持 `("SM","ME")` 而**不支持合并视图 `MT`**,首次检查时 ME 为空(0/180)。

因此先加一个只记日志的探针(每 30 次列举切一次 ME,非空则打 warning),而不是直接实现两趟轮询——覆盖两个存储会让存储索引不再独立标识一条消息,需要改动已持久化的记录形态并做 schema 迁移。探针在列举自身的独占会话内运行、best effort、失败绝不影响所在的列举,且只对 ML307A 开启。等它给出答案再决定是否值得那次迁移。

入站发件人可能是国内格式（3GPP TS 23.040 type-of-number 2，不含国家码），`internal/smscodec` 按规范不为其补 `+`。这意味着同一对端的国内格式与国际格式会落在不同的会话键上；这是既有行为，非本次改动引入。

尚未验证的部分包括：上述转录被拒问题的完整根因与修复后复验、其他收件人的互通、长时间会话稳定性、桥重启或掉线后的恢复行为、经桥完成的完整 SIM AKA 鉴权与 Host VoWiFi 接入、经桥的短信收发、多桥并发。

能力证据规则：型号 adapter 的 `observed` 来自本机直连模组的受控 HIL，不迁移到第三方桥。默认策略把桥设备上所有 `observed` 降级为 `unverified`，因此桥设备只作为物理设备出现，不产生 modem function、SIM 卡槽或 Line，SIM 鉴权返回 `SIM_AKA_UNSUPPORTED`；探测不受影响。配置项 `attestCapabilities` 保留 adapter 状态并在证据文本中标注 operator-attested，这是运维背书而非验证证据，属于记录在案的显式例外。契约细节见 [`remote-at-bridge.md`](remote-at-bridge.md)。

拥有专用 driver transport 的型号（例如 QDC507 的原生 SMS transport）不能经由本路径发布：该 driver 自带 transport，本 seam 无法提供，合成设备会在构造阶段被拒绝。

## Mihomo 专用出口

已经验证：

- 订阅仅作为代理节点来源，Simplus 自己生成 DNS、国家组、TPROXY 和 fail-closed 规则；
- 每次创建、更新或切换都先由当前 Mihomo core 自检，失败配置不会发布或启动；
- 只有一个订阅处于 selected/running 生命周期，切换不会混用多个订阅节点；
- 国家组与 Line 分离，创建 Line 不需要修改或重启 Mihomo 配置；
- 共享 DoH 使用独立节点解析路径，业务出口和 DNS 选择不互相递归；
- `simplusd` 不持有网络 capability，网络对象只由固定 `simplus-netd` API 管理。

节点配置中的 `udp: true`、协议类型、普通 URL-Test、TCP 落地或普通 UDP 成功都不能证明 ePDG 等特定 UDP 业务可用。运行环境必须对目标协议做独立探针，并把失败限制在对应 Line。

## Host VoWiFi

Agent 已移除 IMS 身份中的运营商常量：完整 ISIM 身份优先；没有可用 ISIM 时，只在 EF_AD 明确给出两位或三位 MNC 长度后从 IMSI 派生 Home Domain，无法可靠取得长度时拒绝继续。一张真实 SIM 已在 HIL-0 中确认走无 ISIM 候选的动态派生路径；两位和三位 MNC 的组合仍由 Fixture 覆盖。SIP REGISTER 和 AKA challenge 校验消费这份动态域名。当前真实业务 HIL 仍只覆盖首个已验证运营商的接入 profile；ePDG FQDN、IKE responder identity 和远端选择尚未通用化，因此不能据此声明其他运营商已兼容。

已经在受控硬件上验证：

- 所有现有真实证据均在 SIM READY、RF Off 条件下取得；当前产品已将 RF 与 Host VoWiFi 生命周期解耦，但这不构成 RF On 场景的兼容性结论；
- SIM AKA、IKEv2/ePDG、IMS APN、P-CSCF、Gm transport-mode IPsec 与受保护 REGISTER；
- Digest AKA 的 AUTS/SQN 重同步、服务器重新 challenge、SIM 生成新会话密钥和后续受保护 REGISTER；
- 定期 keepalive、服务器注册周期、提前刷新、有界重连和 XFRM 健康检查；
- ePDG 暂时不可用时按有界退避自动恢复注册，并在恢复后再次完成 SMS over IMS 收发；
- `simplusd` 重启不破坏现有 session；网络 owner 重启后按持久激活意图清理并重建；
- 同一 Line 不会并存多套 namespace、路由、nftables 或 worker；
- Web/API 和普通日志不暴露身份、AKA 材料、内部地址、SPI、节点凭据或完整 SIP 鉴权头。

尚未验证：

- 显式 IMS de-registration；
- 数日稳定性和小时级 IKE/CHILD rekey；
- SMS over IMS 其他收件人互通、来电、拨号、RTP/RTCP 和数字语音媒体；
- RF On 下保持 Host VoWiFi 在线及其与蜂窝注册并存；
- 其他运营商的 ePDG 发现、IKE/IMS 注册和业务纵切；
- 运营商长期策略或所有国家出口组合。

SMS over IMS 的 Fixture 证据覆盖条件 `+g.3gpp.smsip` 注册、SIM 短信中心固定读取、RPDU/SIP 编解码、SIP 接受与异步 RP submit report 分离、数字及 GSM7 字母型发送方、类型化 supervisor API、persist-before-RP-ACK，以及 multipart 分片持久化、歧义拒绝和数据库重开恢复。受控 HIL 已验证真实单段与 multipart 入站、服务请求的关联 RP-ACK 和业务回复、公开 Web/API 的 `unconfirmed → sent` 状态闭环、普通号码单段与两段 UCS-2 自回环，以及自动重连后再次完成同类收发；长短信出站与重组后的入站正文逐字符一致。该证据覆盖 Web/API、业务数据库、typed supervisor 和 IMS worker，但自号码回环不能外推为其他收件人互通。

## 前端与安装

- 管理界面以 React、Vite、React Router、直接 Ant Design 和 TanStack Query 为唯一前端栈；
- 页面通过鉴权 HTTP 读取权威快照和提交 mutation；同源 SSE 只发送有界资源失效与新短信/来电提示，断线后通过 HTTP 收敛；
- 移动端使用响应式布局和抽屉导航，桌面端保持常驻侧边栏；
- Docker Compose 是唯一受支持的 production 部署方式；旧原生 Debian bundle 的历史 smoke
  不构成当前部署支持或 Runtime 证据；
- Debian 13/amd64 的 `simplus-strongswan-plugins` 已从锁定 source/runtime ABI 输入完成普通用户构建，并自动验证 Debian 包身份、依赖范围、导出构造符、动态依赖、固定 runpath、权限、ABI 元数据、SHA-256 manifest 与对应源码完整性；这是包级 Fixture 证据，不是 ePDG/IMS 运行证据；
- netd 镜像通过 `dpkg` 安装该插件包；clean VM 上完整 Compose 安装、升级和卸载 smoke
  仍待完成，旧原生 bundle 或手工复制插件的 smoke 不能替代该项；
- Docker Compose production 已有三镜像构建定义、固定 UID/Unix socket、Agent 无网络与
  单一 sysfs 写点、netd bridge/capability/preflight、bind-mounted 私有数据、幂等管理员
  bootstrap、固定摘要 Mihomo seed 及其对应源码 release workflow；YAML 权限 contract、
  Shell 语法、typed health 和 Go 测试属于 Fixture 证据；
- Compose 文件还通过了 Compose `config --quiet`。三个 production target 已在一台
  Debian 13/amd64 开发宿主本地构建，并以隔离空 USB/sysfs 启动完整 Compose：
  Agent/app/netd typed health、Agent 降权、netd 临时 netns/veth/nft TPROXY/XFRM
  preflight 及清理、首次登录、bootstrap 幂等性和保留数据重建均通过；这是无真实设备
  的 Fixture smoke，不是 clean-VM Runtime 证据；
- 同一类开发宿主还使用校验过的 `v0.1.1` Release 部署包和 GHCR 镜像，完成了既有数据
  的停机快照、恢复、回滚演练、正式切换，以及容器健康、镜像摘要和数据挂载验收。该
  结论只证明现有开发宿主上的生产形态有数据切换，不是 clean-VM 安装、升级、卸载或
  跨环境恢复 Runtime 证据；
- clean Debian VM 生命周期仍未完成；与上述切换验收分开的开发宿主 Compose HIL 已覆盖
  真实 ML307A 只读发现、Mihomo 国家出口、ePDG、Digest AKA AUTS 重同步、Gm XFRM 与
  IMS 注册纵切。Debian 13 的 `iproute2` XFRM selector 使用数值
  UDP 协议号，并在每条 Line 的独立 netns 中以保留 priority/reqid 做幂等清理；随后
  又从公开 Web 完成单段自号码短信回环。该轮容器 HIL 没有请求 RF 写入，自回环不能
  外推为其他收件人互通，也不能替代尚未完成的 clean-VM 生命周期证据；
- ARM64、其他发行版和签名发布链仍不是已承诺支持面。

新的兼容性声明必须写清证据等级。原始证据进入私有记录系统，公开仓库只保留可以由代码、测试或脱敏结论支撑的摘要。
