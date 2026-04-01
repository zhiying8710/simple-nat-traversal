# MiniPunch

一个从零开始的极简内网打洞方案设计仓库。

当前仓库已按要求清空旧代码，只保留新的设计文档，目标是做一个面向 5 台以内设备的小型、安全、带 GUI 的 TCP 端口互联工具，适合通过自有 VPS 组建一个超轻量私网。

## 设计结论

- 形态上不做完整三层 VPN，MVP 只做“远端 TCP 端口映射”。
- 客户端默认只开放本机回环监听，降低误暴露风险。
- 使用自有 VPS 作为控制面、打洞协调器和失败后的中继兜底。
- 优先走 UDP 打洞建立直连，失败后自动切到 VPS 中继。
- 所有业务字节流默认端到端加密，VPS 默认不可见明文；当前 relay 协调元数据仍对 VPS 可见。

## 文档

- [详细设计方案](docs/minipunch-design.md)

## 推荐产品边界

- 支持系统：Windows / macOS / Linux
- 网络规模：最多 5 台设备
- GUI：桌面 GUI + 后台常驻 Agent
- 连接能力：访问其他设备的 TCP 服务，如 `22`、`3389`
- 核心取舍：极简、安全、易部署、易维护

## MVP 方向

第一版建议先完成：

1. VPS 控制面
2. 客户端 GUI + 后台服务
3. 端口发布与访问授权
4. 中继模式打通
5. UDP 打洞直连

这样可以先保证“能用且安全”，再逐步优化“尽量直连”和体验细节。

## 当前实现状态

仓库已经从设计阶段进入 Phase 1 实施阶段，目前已落下这些基础能力：

- Rust workspace 工程结构
- `minipunch-server` 控制面服务
- `minipunch-agent` CLI Agent
- `minipunch-desktop` 桌面配置工作台
- 设备身份生成与签名注册
- Join Token 入网
- 会话心跳
- 网络快照查询
- TCP 服务元数据发布
- 持久化 forward 规则
- 单个 Agent 常驻 `run` 模式
- 常驻 `run` 模式的自动重连、会话刷新与失败恢复
- relay 数据面的设备到设备加密（业务字节流）
- 桌面 GUI 读写 publish / forward 配置
- CLI 和 GUI 的派生状态视图
- 桌面 GUI 接管受管本地 `run` 任务的启动与停止
- 本地 `run` 进程持续写入 runtime state 文件
- 桌面 GUI 观测真实本地 `run` 状态、最近事件和重启原因
- `run` 模式按配置托管 direct-enabled published services 和 `transport=auto` 的 forward 规则
- 控制面只同步服务标识与 ACL，不再上送或存储服务真实 `target host/port`
- direct rendezvous 协调骨架（创建尝试、轮询 pending、交换 UDP 候选地址）
- signed direct UDP probe（真实 socket `hello/ack` 探测）
- `direct-connect / direct-serve` 自动化工作流
- experimental direct TCP proxy data plane（`direct-tcp-forward / direct-tcp-serve`）
- relay WebSocket 数据面支持 batched multiplexing，多条 channel envelope 可合并进单个 frame
- `auto-forward / auto-serve` 自动链路选择与 relay 回落
- `auto` forward 在 relay fallback 期间按退避节奏继续重试 direct
- `auto` forward 在 relay 仍有活跃连接时不会粗暴拿掉旧 relay 连接；它可以先在后台 prewarm direct，再在合适时机切 ingress
- `auto` forward 在 source 侧已经收敛成单本地 listener，relay/direct handoff 只切 ingress，不再重新 bind 本地端口
- `auto` forward 在 relay idle 时先 prewarm direct，再切 ingress 到 direct，进一步缩小 handoff 抖动窗口
- `auto` forward 在 relay drain 期间补了 per-connection handoff fallback；如果某个新连接在 direct `channel_open` 前失败，还能就地借老 relay 接住这次连接
- target 侧 direct responder 已经不会被单条 rendezvous 串行卡住；同一个 `direct_udp_bind_addr` 现在可以作为 shared UDP hub 并发承接多条 rendezvous
- `direct-tcp-serve` 遇到失败的 rendezvous 后会继续轮询后续 attempt，不再一条失败就直接退出
- direct TCP 数据面的最小可靠层（seq/ack、range-compressed 少量 SACK selective recovery、sender-side SACK scoreboard、统一的 scoreboard-driven loss recovery selector、基于 SACK 证据逐步推进到更后续 hole 的 fast retransmit、scoreboard-aware timeout retransmit、Reno 风格窗口控制、RTT 感知重传、重复 ACK 快速重传、接收侧乱序缓冲、close-ack、channel keepalive / keepalive-ack、peer idle timeout）
- runtime state / GUI 的 direct 指标观测（`cwnd`、`ssthresh`、`RTO`、smoothed RTT、pending packets、fast recovery、keepalive counters）
- `scripts/check_direct_regressions.sh` / `scripts/smoke_direct_only.sh` / `scripts/smoke_auto_handoff_fallback.sh` 三层 direct 回归入口
- 桌面端系统托盘（关窗进托盘、托盘恢复窗口、托盘启停受管 agent、托盘退出）
- 桌面端自启动管理（macOS `launchd` / Linux XDG autostart / Windows Startup folder）
- `scripts/package-macos.sh` / `scripts/package-linux.sh` / `scripts/package-windows.ps1` 三套桌面打包脚本
- `scripts/package-linux-server.sh` Linux 服务端打包脚本
- GitHub Actions 直接发版工作流（输入版本号后直接创建 Release 并上传 Linux server + macOS/Windows client 包）

已经打通但仍属早期版本：

- 基于 VPS relay 的 WebSocket 数据面
- 本地 TCP 监听到远端已发布 TCP 服务的转发

说明：

- 当前 GUI 已迁移为 `Tauri + Vue` 桌面端，页面结构按总览 / 控制面 / 已发布服务 / 转发规则 / 在线设备 / 日志 / 原始配置拆分，底层继续复用现有 `core / server / agent` Rust 分层。
- 当前 GUI 已经可以为 published service 配置 direct responder 参数，也可以为 forward rule 配置 `relay / auto` 传输策略、UDP bind、candidate type 和 direct wait seconds。
- 当前 GUI 已经支持 `Refresh Status`，会结合本地配置和网络快照展示设备在线情况、服务是否已同步、forward 规则是否 ready / target_offline / service_missing。
- 当前 GUI 已经按总览 / 控制面 / 已发布服务 / 转发规则 / 在线设备 / 日志 / 原始配置拆成多个 tab，不再把所有编辑和观测内容堆在同一页。
- 当前 GUI 现在进一步拆成总览 / 控制面 / 已发布服务 / 转发规则 / 在线设备 / 日志 / 原始配置 7 个 tab；运行观测、链路统计和最近事件集中放到“日志”页，配置文件原文放到“原始配置”页。
- 当前 GUI 对“加载配置 / 保存配置 / 加入网络 / 发送心跳 / 读取网络 / 刷新状态 / 同步服务”等动作补了统一 loading 和结果提示，执行期间会禁用相关按钮并在界面顶部显示完成结果。
- 当前 GUI 已经可以启动、停止桌面端自己拉起的受管 `minipunch-agent run` 任务，并显示这个受管任务的运行态。
- 当前 GUI 已经补了系统托盘；如果托盘可用，关窗会默认转成“隐藏到托盘”，并可以从托盘恢复窗口、启停受管 agent、切换自启动或退出程序。
- 当前桌面端支持 `--autostart --config <path>` 启动参数；自启动入口会用它在登录后以后台模式拉起受管 agent，同时仍兼容旧的 `--background --start-agent --config <path>` 入口。
- 当前新设备首次入网后拿到的 `session_expires_at` 会继承它所消费 join token 的截止时间，不再固定写死为 24 小时；已在库的老设备若还没有迁移出的设备级 session deadline，则继续兼容旧的 24 小时 session 逻辑，直到重新入网。
- 当前任意本地 `minipunch-agent run` 只要使用同一份配置，都会持续刷新同目录下的 `<config-stem>.runtime.json`；GUI 会读取它来展示真实运行状态、最近心跳、重启原因和最近事件。
- 当前 GUI 在启动时会忽略上次关闭遗留的 terminal / stale runtime state，避免仅仅重开桌面端时仍把旧的本地运行观测当成“当前正在运行”。
- 当前 GUI 新增了在线设备页：会基于最近一次 network snapshot 列出在线设备和它们已发布的服务，并支持一键生成本地 forward 草稿，再跳转到转发规则页继续调整。
- 当前 GUI 会把界面上展示的控制面时间、运行态时间和事件时间统一格式化成 `YYYY-MM-DD HH:MM:SS`，不再直接显示 Unix 时间戳。
- 当前总览页会集中展示设备 / 会话 / 已发布 / 转发 / 受管运行 / 运行观测摘要，顶部不再重复堆相同信息。
- 当前 `<config-stem>.runtime.json` 还会记录每条 enabled forward rule 和每个 published service 的运行态观测，包括 configured transport、active transport、当前 state、最近错误、direct attempt / relay fallback 次数、direct / relay 连接计数、forward 级 active connection count、最近对端，以及最近一次失败落在哪个阶段（如 `rendezvous_wait`、`probe`、`channel_open`、`data_plane`）。
- 当前 published service 侧还会记录 `active_session_count`，用于表示当前有多少条 direct session 正在活跃；GUI 的 `Observed Service Transports` 也会直接展示这个值。
- 当前 `<config-stem>.runtime.json` 对 direct 链路还会额外记录最近一次活跃 direct channel 的运行指标，例如 `cwnd`、`ssthresh`、`RTO`、smoothed RTT、待发/待收分片数、是否处于 fast recovery，以及 keepalive 发包/回包计数；GUI 的 `Observed Forward Transports` / `Observed Service Transports` 也会直接显示这些值。
- 当前 GUI 可以为当前配置直接启用或禁用自启动；它会按平台写入 macOS `~/Library/LaunchAgents/minipunch-desktop.plist`、Linux `~/.config/autostart/minipunch-desktop.desktop` 或 Windows Startup folder 下的 `minipunch-desktop.vbs`。
- 当前桌面端目录结构已经切成标准 `Tauri` 形态：前端在 [`apps/minipunch-desktop/src`](/Users/zhiying8710/wk/simple-nat-traversal/apps/minipunch-desktop/src)，后端在 [`apps/minipunch-desktop/src-tauri`](/Users/zhiying8710/wk/simple-nat-traversal/apps/minipunch-desktop/src-tauri)，发布时需要先执行前端构建，再编译 `minipunch-desktop`。
- 当前 relay 数据面已经对 `ChannelData` 做了设备到设备加密，`TLS/WebSocket` 仍作为传输层保护；同时 relay writer 现在会把多条 envelope batched 进单个 WebSocket frame，降低高频小包时的帧开销。
- 当前服务真实 `target host/port` 已不再上送控制面，也不会出现在 network snapshot 或 relay open 流程里；target 侧 relay/direct responder 都改为按 `service_id` 在本地配置里解析真实落点。
- 当前服务名、ACL、源/目标设备 ID、rendezvous 候选地址等 relay 协调元数据仍对 VPS 可见。
- 当前 CLI 已支持更稳的单个常驻 `run` 模式：会在 relay 断开后自动重连、在会话接近过期时自动重建后台任务，并自动同步配置里的已发布服务。
- 当前每条 forward 规则会独立后台重试；坏规则会持续告警和退避重试，但不会再把整个 `run` 进程拖垮。
- 当前 `run` 模式已经会按配置托管 direct responder：对 `direct_enabled=true` 的 published service 轮询 pending rendezvous，并在 ready 后切入 direct TCP 数据面。
- 当前 `run` 模式里的 target 侧 direct responder 已经把每个 published service 的 `direct_udp_bind_addr` 收敛成 shared UDP hub：并发 direct rendezvous 会共用同一个固定 UDP 入口，不再靠同 IP 临时端口来躲开串行阻塞。
- 当前 `run` 模式已经会按 forward rule 的 `transport_mode` 选择数据面：`relay` 继续走 relay supervisor，`auto` 会先试 direct，失败后自动回落 relay。
- 当前 `auto` forward 不会在切到 relay 后一直卡住；当 relay fallback 正常工作时，仍会按 `5s -> 10s -> 20s -> ...` 的退避节奏再次尝试 direct，并把 `direct_retry`、最近失败阶段和下一次重试节奏写进 runtime state。
- 当前 `auto` forward 如果发现 relay 还有活跃连接，不会为了重试 direct 立刻把 relay listener 拿掉；它可以先在后台发起 `prewarming direct rendezvous in parallel`，等 direct path 准备好后再切 ingress，而旧 relay 连接继续自然 drain。
- 当前 source 侧 `auto` forward 已经把本地入口收敛成单 listener；在 relay/direct handoff 时，不再靠重新 `bind` 本地端口切换数据面，而是保留同一个本地监听口，只切内部 ingress。
- 当前 `auto` forward 在 relay 已空闲时，不再先关 listener 再慢慢做 rendezvous / probe；它会先把 direct path 预热到 `direct_ready`，然后再把同一个本地 listener 的 ingress 从 relay 切到 direct，减少 idle handoff 时的不可用窗口。
- 当前如果 relay 还在 drain，而某个“刚切到 direct 后的新连接”在 direct `channel_open` 前就失败，source 侧会把这次连接临时回落到仍存活的 relay，并在 relay 完全 drain 后把状态收敛回“direct 已成为 sole ingress”。
- 当前仓库已提供 [`scripts/check_direct_regressions.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/check_direct_regressions.sh)，作为更轻的 direct 回归入口：默认跑纯逻辑 direct 测试和 `cargo check --workspace`，设置 `RUN_DIRECT_SMOKE=1` 可串上纯直连 smoke，设置 `RUN_HANDOFF_SMOKE=1` 时再串上完整 handoff smoke。
- 当前仓库已提供 [`scripts/smoke_direct_only.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/smoke_direct_only.sh)，可以自动复现“target direct-enabled -> source auto forward 选中 direct -> 本地端口成功拉起直连 HTTP / 大 payload”这条更轻的本地回归路径。
- 当前仓库已提供 [`scripts/smoke_auto_handoff_fallback.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/smoke_auto_handoff_fallback.sh)，可以自动复现“relay-only 起步 -> slow relay drain -> target direct responder 拉起 -> `direct_handoff_fallback` 命中 -> relay drain 完成后回到 `direct_active`”这条本地回归路径。
- 当前仓库已提供 [`scripts/package-macos.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/package-macos.sh)、[`scripts/package-linux.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/package-linux.sh) 和 [`scripts/package-windows.ps1`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/package-windows.ps1)，分别生成 macOS `.dmg`、Linux `.tar.gz` 和 Windows `.zip` 桌面分发包；其中 macOS 包现在会做本地 ad-hoc 签名，macOS/Linux 打包脚本也都做过本地验证，但这些包仍未做 Apple notarization 或正式发行级签名。
- 当前仓库已补桌面端图标资源链路：[`icon.svg`](/Users/zhiying8710/wk/simple-nat-traversal/apps/minipunch-desktop/assets/icon.svg) 作为源文件，经 [`generate-desktop-icons.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/generate-desktop-icons.sh) 生成 PNG / ICNS / ICO；macOS `.app` 会携带 `AppIcon.icns`，Windows 可执行文件在构建时会嵌入 `AppIcon.ico`。
- 当前 Windows 桌面端已经启用 GUI 子系统，正常启动不再弹出 cmd 窗口；Windows 自启动入口也从 Startup folder 的 `.cmd` 改成了隐藏式 `.vbs` launcher，并会在登录后自动拉起受管 agent。
- 当前 Windows 自启动在进入后台前会先做配置预检；如果当前配置还没完成入网、缺少设备身份材料，或关键字段不完整，桌面端会直接退出进程，不会残留一个隐藏但不可用的托盘进程。
- 当前仓库还提供 [`scripts/package-linux-server.sh`](/Users/zhiying8710/wk/simple-nat-traversal/scripts/package-linux-server.sh)，用于生成仅包含 `minipunch-server` 和 systemd 示例单元的 Linux server `.tar.gz` 发布包。
- 当前仓库已补 [`release.yml`](/Users/zhiying8710/wk/simple-nat-traversal/.github/workflows/release.yml) 手动发版工作流：输入稳定版本号 `x.y.z` 后，会按 `v<version>` 生成 release tag，并先检查现有 tag；若存在更高版本 tag 会直接失败，若存在同版本 tag/release 会先删除，再直接创建 GitHub Release，并分别上传 Linux server、macOS client、Windows client 三个发布包；整个流程不依赖 Actions artifact 中转。
- 当前发版工作流还会额外校验两件事：必须从默认分支触发，以及输入版本号必须同时匹配 `Cargo.toml` 的 workspace version 和 `apps/minipunch-desktop/package.json` 的 version。
- 当前发版工作流已经补了 Node/Rust 构建缓存，并且如果构建都成功但最终发布失败，也会清理 draft release 和新建 tag，避免残留半成品 release。
- 当前 GUI 状态面板已经拆成两部分：一部分是“本地配置 + 网络快照”的派生视图，另一部分是读取 `<config-stem>.runtime.json` 得到的真实本地运行观测。
- 当前 GUI 读取 runtime state 后，已经能单独展示 `Observed Forward Transports` 和 `Observed Service Transports`，直接看出某条链路当前实际跑在 direct 还是 relay，以及 direct / relay 连接次数、forward 级 active connection count、最近 transport switch、最近对端和最近一次失败阶段。
- 当前 runtime state 里的 forward state 除了 `relay_active / direct_active / direct_retry_deferred`，还会出现 `direct_ready`、`direct_handoff_fallback` 这类 handoff 过渡状态，用来表示 direct 已经预热完成、正在借旧 relay 兜住单次连接等细节。
- 当前这套真实运行观测还是“状态文件 + 最近事件”方式，不是 OS 级进程附着；如果进程被强杀，GUI 会在状态文件长时间不更新后把它显示为 `stale`。其中 direct 指标是最近一次活跃 direct channel 的观测值，不是内核级实时 counters。
- 当前已经补了 direct rendezvous 协调接口，源端可以创建一次直连尝试并提交自己的 UDP 候选地址，目标端可以轮询 pending 尝试、提交自己的候选地址，双方都能查询对端候选。
- 当前 agent 已经可以在 ready 的 rendezvous 上创建真实 UDP socket，对 peer candidates 发送带设备身份签名的 `hello/ack` 探测包，并选出一个可达 candidate。
- 当前 CLI 已经支持 `direct-connect / direct-serve`，可以把“start / announce / probe”收敛成源端一个命令、目标端一个常驻命令。
- 当前 CLI 已经支持 `direct-tcp-forward / direct-tcp-serve`，可以把本地 TCP 连接通过一条 direct UDP channel 代理到远端已发布服务。
- 当前 CLI 已经支持 `auto-forward / auto-serve`：源端会先尝试 direct，若 rendezvous / probe / direct channel 建立失败，则自动回落到 relay。
- 当前这条 direct TCP 数据面还是实验性的，但已经补上了较完整的最小可靠层：每个 channel 现在会做顺序号、累计 ack、range-compressed 少量 SACK selective recovery、sender-side SACK scoreboard、统一的 scoreboard-driven loss recovery selector、基于 SACK 证据逐步推进到更后续 hole 的 fast retransmit、scoreboard-aware timeout retransmit、Reno 风格的 slow start / congestion avoidance / fast recovery、RTT 感知重传、重复 ACK 快速重传、接收侧乱序密文缓冲、close-ack 关闭握手，以及 channel keepalive / keepalive-ack 和 peer idle timeout。
- 当前 direct 数据面仍然偏保守：虽然 recovery、空闲保活和观测都已经成型，但它依然是用户态 TCP-over-UDP 实验实现，吞吐、抖动、跨 NAT 长期 soak 和极端网络下的稳定性还没有按正式产品继续打磨。
- 当前 target 侧已经在“单个 published service + 单个 `direct_udp_bind_addr`”这个粒度上做成了 shared UDP hub；不过它还不是整机范围的全局 UDP ingress，多 service 之间仍然各自维护自己的 direct UDP 入口。
- `relay-serve` / `forward` 仍保留用于调试和拆分联调。

## 目录

- `apps/minipunch-server`
  服务端二进制入口
- `apps/minipunch-agent`
  Agent CLI 二进制入口
- `apps/minipunch-desktop`
  Phase 1 桌面 GUI 壳
- `crates/minipunch-core`
  共享协议、设备身份、ID 与签名工具
- `crates/minipunch-server`
  服务端控制面、SQLite、路由
- `crates/minipunch-agent`
  Agent 配置、注册、心跳、服务发布逻辑

## 快速启动

启动服务端：

```bash
cargo run --bin minipunch-server -- --listen-addr 127.0.0.1:9443 --database ./minipunch.db
```

初始化服务端，拿到管理员 Token 和第一枚 Join Token：

```bash
curl -X POST http://127.0.0.1:9443/api/v1/bootstrap/init
```

用 Join Token 初始化本机 Agent：

```bash
cargo run --bin minipunch-agent -- init \
  --server-url http://127.0.0.1:9443 \
  --join-token <join_token> \
  --device-name my-laptop
```

发送一次心跳：

```bash
cargo run --bin minipunch-agent -- heartbeat
```

查看网络快照：

```bash
cargo run --bin minipunch-agent -- network
```

发布一个 TCP 服务：

```bash
cargo run --bin minipunch-agent -- publish \
  --name ssh \
  --target-host 127.0.0.1 \
  --target-port 22 \
  --allow dev_xxx
```

增加一个持久化 forward 规则：

```bash
cargo run --bin minipunch-agent -- add-forward \
  --name office-ssh \
  --target-device dev_xxx \
  --service ssh \
  --local-bind 127.0.0.1:10022
```

启动单个常驻 Agent：

```bash
cargo run --bin minipunch-agent -- run
```

启动桌面壳：

```bash
cd apps/minipunch-desktop
npm install
npm run build
cd ../..
cargo run --bin minipunch-desktop
```

桌面端当前支持：

1. 从指定 `agent.toml` 路径加载配置
2. 编辑并保存多条 published services
3. 编辑并保存多条 forward rules
4. 触发 `Join Network`、`Heartbeat`、`Load Network`
5. 将当前 published services 一键同步到控制面
6. 刷新派生状态视图，查看设备 / service / forward 的当前健康状态
7. 启动和停止桌面端受管的本地 `run` 任务
8. 读取本地 runtime state 文件，查看最近心跳、重启原因和运行事件
9. 使用系统托盘控制窗口显示、受管 Agent 和开机自启

桌面端开发期常用命令：

```bash
cd apps/minipunch-desktop
npm install
npm run tauri:dev
```

## Direct Rendezvous

当前已经可以用控制面和 CLI 手动走通一轮 UDP 直连前的 rendezvous 协调：

1. 源设备创建一次直连尝试，并带上自己观察到的 UDP 候选地址
2. 目标设备轮询 pending 直连尝试
3. 目标设备提交自己的 UDP 候选地址
4. 双方查询同一个 rendezvous，拿到对端候选并进入 `ready`

CLI 示例：

```bash
cargo run --bin minipunch-agent -- rendezvous-start \
  --target-device <target_device_id> \
  --service ssh \
  --candidate public=198.51.100.10:40000 \
  --candidate local=10.0.0.2:40000

cargo run --bin minipunch-agent -- rendezvous-pending

cargo run --bin minipunch-agent -- rendezvous-announce \
  --rendezvous-id <rendezvous_id> \
  --candidate public=203.0.113.20:45000 \
  --candidate local=192.168.1.20:45000

cargo run --bin minipunch-agent -- rendezvous-get \
  --rendezvous-id <rendezvous_id>
```

说明：

- `--candidate` 支持 `type=ip:port` 形式，例如 `public=1.2.3.4:40000`、`local=192.168.1.5:40000`
- 如果不带 `type=`，CLI 会默认把它当成 `manual`
- 当前可以继续在 ready 的 rendezvous 上执行 `direct-probe`

```bash
cargo run --bin minipunch-agent -- direct-probe \
  --rendezvous-id <rendezvous_id> \
  --bind 0.0.0.0:40000 \
  --wait-seconds 8
```

- `direct-probe` 会轮询 rendezvous 到 `ready`，然后绑定本地 UDP 端口，对 peer candidates 循环发送签名 `hello`，收到已验证的 `hello` 或 `ack` 后返回选中的 peer candidate

如果不想手工拆命令，现在也可以直接用自动化工作流：

```bash
# target 设备
cargo run --bin minipunch-agent -- direct-serve \
  --service ssh \
  --bind 192.168.1.20:41022 \
  --wait-seconds 8

# source 设备
cargo run --bin minipunch-agent -- direct-connect \
  --target-device <target_device_id> \
  --service ssh \
  --bind 192.168.1.10:41021 \
  --wait-seconds 8
```

- `direct-serve` 会轮询指向本机该 service 的 pending rendezvous，自动 announce 本地 UDP candidate，并继续完成 probe
- `direct-connect` 会自动创建 rendezvous、提交源候选地址并等待 probe 结果
- 当前 `--bind` 既是本地 UDP 绑定地址，也是公告给对端的 candidate，所以这里要填对端可达的真实地址，不能直接写 `0.0.0.0`

如果要直接把一条本地 TCP 代理链路跑到实验性的 direct 数据面，可以这样：

```bash
# target 设备
cargo run --bin minipunch-agent -- direct-tcp-serve \
  --service web \
  --udp-bind 192.168.1.20:41042 \
  --wait-seconds 8

# source 设备
cargo run --bin minipunch-agent -- direct-tcp-forward \
  --target-device <target_device_id> \
  --service web \
  --local-bind 127.0.0.1:19096 \
  --udp-bind 192.168.1.10:41041 \
  --wait-seconds 8
```

- `direct-tcp-forward` 会创建 rendezvous、完成 probe，然后在本地监听 `--local-bind`，把接入的 TCP 流封装进 direct UDP channel
- `direct-tcp-serve` 会等待对应 service 的 direct 尝试，完成 probe 后接受 direct channel，并把数据桥接到本机 service 的 `target_host:target_port`
- 当前 direct channel 已经会为数据帧附带顺序号，并通过累计 ack 做 Reno 风格窗口控制、RTT 感知 RTO、重复 ACK 快速重传、最老超时分片优先重传和接收侧乱序缓冲的最小可靠传输；这比之前的裸 UDP datagram、stop-and-wait、固定窗口和简化 AIMD 都更稳、更快，但仍然没有 selective recovery 和更细粒度的恢复策略

如果你想让源端自动“先试直连，不行再走 relay”，现在可以直接用：

```bash
# target 设备
cargo run --bin minipunch-agent -- auto-serve \
  --service web \
  --udp-bind 192.168.1.20:41042 \
  --direct-wait-seconds 5

# source 设备
cargo run --bin minipunch-agent -- auto-forward \
  --target-device <target_device_id> \
  --service web \
  --local-bind 127.0.0.1:19096 \
  --udp-bind 192.168.1.10:41041 \
  --direct-wait-seconds 5
```

- `auto-serve` 会同时保留 relay 服务面，并轮询该 service 的 pending direct 尝试；一旦 ready 就进入 direct TCP 数据面
- `auto-forward` 会优先创建 rendezvous 并尝试 direct；如果超时或 direct 失败，会自动切到 relay 并继续在同一个 `--local-bind` 上提供访问
- 现在也可以把这些参数直接写进 `agent.toml`，交给 `run` 和桌面 GUI 配置工作流统一托管

## Relay 数据面

当前已经支持 relay-only 的真实 TCP 转发，并且 relay 上承载的业务字节流已经做了设备到设备加密。

推荐工作流已经从“手动分别启动 relay 和 forward”升级为“写入 forward 规则后直接运行单个 Agent 进程”。

补充说明：

- 当前 `run` 模式里的 forward rule 默认 `transport_mode=relay`
- 如果某条 forward rule 配成 `transport_mode=auto`，`run` 会先试 direct，再在失败时自动回落 relay
- 如果某个 published service 配成 `direct_enabled=true`，`run` 会同时托管它的 direct responder

`run` 模式当前的恢复行为：

1. relay WebSocket 断开后会自动重连
2. 配置里的已发布服务会在重连或会话刷新后自动同步到服务端
3. forward 规则会独立做服务解析和退避重试
4. 单条 forward 规则失败不会导致整个 Agent 退出

推荐方式：

1. 目标设备发布服务
2. 源设备写入 forward 规则
3. 两端都运行 `cargo run --bin minipunch-agent -- run`
4. 源设备本机直接访问配置的回环端口

示例：

```bash
cargo run --bin minipunch-agent -- add-forward \
  --name target-web \
  --target-device <target_device_id> \
  --service web \
  --local-bind 127.0.0.1:19091

cargo run --bin minipunch-agent -- run
```

如果你想让 `run` 模式直接接管 auto transport，可以这样写：

```bash
# target 设备
cargo run --bin minipunch-agent -- publish \
  --name web \
  --target-host 127.0.0.1 \
  --target-port 18080 \
  --allow <source_device_id> \
  --enable-direct \
  --udp-bind 192.168.1.20:41042 \
  --candidate-type local \
  --direct-wait-seconds 5

# source 设备
cargo run --bin minipunch-agent -- add-forward \
  --name target-web-auto \
  --target-device <target_device_id> \
  --service web \
  --local-bind 127.0.0.1:19096 \
  --transport auto \
  --udp-bind 192.168.1.10:41041 \
  --candidate-type local \
  --direct-wait-seconds 5

# 两端都运行
cargo run --bin minipunch-agent -- run
```

当前这套 `auto` 规则如果第一次 direct 没打通，不会只停在 relay；source 会先切到 relay 保证可用，然后在 relay 还健康时按退避节奏再次尝试 direct。

目标设备启动 relay 服务循环：

```bash
cargo run --bin minipunch-agent -- --config /path/to/target.toml relay-serve
```

源设备启动本地转发：

```bash
cargo run --bin minipunch-agent -- --config /path/to/source.toml forward \
  --target-device <target_device_id> \
  --service ssh \
  --local-bind 127.0.0.1:10022
```

然后本机直接访问：

```bash
ssh -p 10022 127.0.0.1
```

一个本地 HTTP 冒烟例子：

1. 目标设备先发布 `web -> 127.0.0.1:18080`
2. 目标设备运行 `relay-serve`
3. 源设备运行 `forward --target-device <target> --service web --local-bind 127.0.0.1:19090`
4. 源设备访问 `http://127.0.0.1:19090`

这条链路，以及新的 `add-forward + run` 单进程工作流，都已经在本仓库本地冒烟测试中跑通；最近一次验证使用两台 Agent、本地 HTTP 服务和加密 relay 链路，源端访问本机 `127.0.0.1:19092` 成功返回目标端 HTTP 200。另做过一轮恢复性测试：源端同时存在一条有效 forward 规则和一条无效规则时，无效规则会后台持续重试，而有效规则仍可正常转发。
