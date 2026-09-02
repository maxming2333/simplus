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
| 提示符类命令（命令 → `>` → 载荷 → 终止状态）经桥的原子交换，含「载荷未发出」与「结果不确定」的状态分类 | HIL-0 |
| ML307A 蜂窝短信完整回环经桥：出站提交 → 存储投递 → 列举 → PDU 读取 → ack 删除 | HIL |
| 多条消息共存时的列举与按索引读取/删除 | HIL |
| 一次操作复用一个桥会话（列举/读取/删除/多段提交），独占窗口为操作级 | Fixture + HIL-0 |
| 桥固件胖/瘦模式切换真实改变模组上报设置 | HIL-0 |

HIL-0 证据取自一枚 ML307A-DSLN-MTSH1S00 与一台参考 ESP32 桥固件，可通过 `SIMPLUS_REMOTE_AT_HIL_CONFIG` 重放（只读，不含写入/射频/SIM 变更）。

入站短信的消费归属由桥固件的胖/瘦模式决定，二者互斥。胖模式（默认）下新短信直投桥固件、不进模组存储，因此 Simplus 的存储列举永远为空；只有瘦模式才把短信留在存储里交给 Simplus。误判这一点会表现为「Simplus 收不到短信」但两侧日志都正常，细节见 [`remote-at-bridge.md`](remote-at-bridge.md)。

ML307A 蜂窝短信已通过自身 HIL 并提升为 `observed`：一张已驻网的指定 SIM（LTE，`+CEREG` stat=1）经桥完成一次完整自号码回环——出站 submit-prompt 提交拿到 `+CMGS` 参考号，入站约 1 秒落入 SIM 存储，列举后按索引读取且解码正文与唯一标记完全一致，ack 后该索引从存储中消失，其余待处理消息不受影响。测试为 opt-in 且需两个环境变量（配置 + 本机号码），是本仓库唯一有外部副作用的 HIL。

通用 3GPP PDU 模式 driver、durable recovery store 与提交/确认状态机由
`internal/modemadapter/standardsms` 与首个通过蜂窝短信 HIL 的型号共用；共用代码不等于共用证据，上述提升依据的是 ML307A 自己的这次验收。桥设备另有独立的证据策略：未经运营者显式背书时，桥上所有 `observed` 一律降级，因此本次提升本身不会让一台桥接模组变为可操作。

**已知未闭环的可靠性问题**：同一回环在 5 次尝试中有 2 次以「AT 转录非法」被拒。已确认的一个成因是模组自身重启后发出的 `+MATREADY` 上报——桥固件此前不识别它，会把它并入某条在途命令的响应并使其解析失败（实测 `AT+CPMS?` 返回 `+MATREADY` + `+CPMS: ...`）。桥固件已修复该前缀识别并在检测到模组重启后重新下发模组配置（`CNMI`/`CMGF`/`CGDCONT` 都不写入模组 NVM），但**尚未在真机上复验**。是否还有第二个成因未确定。上层行为是 fail-closed 的（拒绝无法解析的转录、不猜测），因此该问题表现为一次同步周期被跳过，不会造成消息丢失或重复。

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
