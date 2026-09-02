# 0028：远程 AT 桥配置归 Agent 私有文件，不进 Web/API

- 状态：Accepted / Implemented
- 日期：2026-09-01

## 背景

`internal/atremote` 让 `simplus-agent` 通过 HTTP 驱动一台独占持有模组 AT 串口的远程桥。
运维随之提出一个自然的诉求：既然网页上已经能「添加模组」「添加 Line」，桥的地址与凭据
是否也应该在网页上填。

USB 路径提供不了可对齐的先例，因为它**没有任何设备级配置**：`hardwareprobe.scanHardware`
直接读 sysfs 的 `idVendor`/`idProduct`/厂商字符串并交给 registry 匹配，插上即出现、拔掉即
消失。网页上为 USB 设备配置的是业务意图（`AddManagedModemRequest` 只接 `candidateId`，
`AddManagedLineRequest` 只接 `candidateId` + `displayName`），而不是设备事实。

桥没有总线可枚举，因此「这里有一台某型号模组，地址与凭据如下」只能由人**断言**。这条
断言属于硬件事实层，不属于业务意图层。

## 决定

1. 桥配置只来自 `simplus-agent` 启动时读取的一个私有 JSON 文件
   （`-remote-at-config` / `SIMPLUS_AGENT_REMOTE_AT_CONFIG`）。它必须是常规文件、非符号
   链接、`mode & 0o077 == 0`、大小有界、拒绝未知字段。不使用 flag 或环境变量承载凭据：
   命令行对本机任何进程可读。
2. 公共 HTTP API 与 Web 不得出现任何控制端点地址、主机、凭据或桥标识输入。
   `attransport.Query` 保持只对编译进来的型号 adapter 可见这一不变量不变。
3. 桥设备进入清单后，其**业务意图**配置与 USB 设备完全一致：从候选「添加模组」，再从
   该模组的 SIM/Profile 候选「添加 Line」，只填显示名。这一层不存在差异，也不需要对齐。
4. 能力背书（`attestCapabilities`）必须与桥定义写在同一份私有文件里。它把 adapter 的
   `observed` 证据保留给一条没有 Simplus 受控 HIL 的控制路径，属于运维背书而非验证证据，
   因此不得成为浏览器里的一个复选框。
5. 只读可见性由已有渠道提供，不新增 Web 面：Agent 启动时按桥打印 key/profile/host 与
   明文、背书两类告警；`simplusctl health agent` 列出桥设备（`PhysicalPath` 为 key，
   VID:PID 为空——空描述符本身就是「非 USB 控制路径」的诚实信号）。
   **不为此在 `agentapi`、`hardware` 领域模型或公共 API 增加字段**：那会把「远程 vs 本地」
   泄漏到上层，正是本方案赖以成立的不变量。
6. 变更一个桥 = 改文件 + 重启 agent 容器。这不是遗漏，见下节。

## 理由

- 分层：`inventory`/`line` 消费稳定业务身份，硬件事实与控制路径终止在 Agent 与 adapter
  边界（`docs/architecture.md`、`.trellis/spec/core/backend/application-boundaries.md`）。
  桥地址是控制路径事实，放进业务面会让 Web 调用方间接决定 AT 命令的去向。
- 组合期不变量：`modemadapter.NewRegistry` 在启动时一次性组合并对 profile 重复/规则重叠
  fail closed，其结果被 scanner、monitor 与 SMS backend 共同持有。运行时可变的 adapter
  选择会削弱这条 fail-closed 设计。
- 容器隔离：`compose.yaml` 里 `agent` 读写 `./data/agent`、`app` 读写 `./data/core`，两者
  没有共享可写路径，`agent-runtime` 卷 app 只读挂载且只含 socket。`simplusd` 因此在物理上
  写不到 Agent 的私有配置——这是既有的隔离契约，不是本方案新增的限制。
- 凭据与证据：桥凭据与能力背书都属于「只有能登上宿主的人才应改动」的量级。

## 后果

- 新增或修改桥需要宿主访问权与一次 agent 重启，不能由 Web 管理员完成。这是刻意的成本。
- 桥可达性不出现在网页上；不可达的桥表现为一条可重试的类型化探测错误，与拔掉的 tty
  端点完全一致，上层无法区分二者。
- 若将来确实要开放网页写入，必须另立决策并至少解决：SSRF containment（Agent 会带凭据向
  配置的地址发起请求）、能力背书的归属（浏览器是否有权提升证据等级）、Agent socket 上一条
  有界写操作、以及运行时替换 opener 目标表的并发正确性。本记录不预先批准其中任何一项。

## 替代方案

- **网页可写**：见上，四项前置未解决前不采纳。
- **网页只读展示桥主机/可达性**：需要在 `agentapi`/领域模型/公共 API 增加「这是桥接控制
  路径」字段，直接违反第 5 条所保护的不变量。可见性改由 Agent 日志与 `simplusctl` 承担。
- **flag 或环境变量承载凭据**：命令行与环境对本机进程可读，拒绝。
- **让 `simplusd` 写 Agent 配置文件**：需要在两个容器间开一条共享可写路径，削弱现有隔离
  契约，且仍要重启 agent 才生效。
