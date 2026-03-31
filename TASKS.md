# MiniPunch Tasks

本文件用于持续跟踪当前实现状态，后续开发会及时更新。

## 状态说明

- `[done]` 已完成并至少做过一次本地验证
- `[doing]` 正在进行
- `[next]` 下一批优先任务
- `[later]` 明确要做，但不在最近一批

## 当前阶段

- `[doing]` 核心数据面、桌面交付和打包脚本已收敛，进入验收与后续产品化打磨阶段

## 已完成

- `[done]` 清空旧仓库代码并重建为新项目结构
- `[done]` 完成总体设计文档
- `[done]` 建立 Rust workspace、shared core、server、agent、desktop 壳
- `[done]` 完成设备身份生成、签名注册、Join Token 入网
- `[done]` 完成控制面 SQLite 存储、设备列表、网络快照、服务发布
- `[done]` 完成 relay-only WebSocket 数据面
- `[done]` 完成本地 TCP 监听到远端已发布 TCP 服务的 relay 转发
- `[done]` 完成 relay 数据面的本地端到端 HTTP 冒烟测试
- `[done]` forward 规则持久化到 Agent 配置文件
- `[done]` 完成 `minipunch-agent run` 单进程常驻模式
- `[done]` 完成 `add-forward + run` 工作流的本地端到端冒烟测试
- `[done]` relay 数据面补设备到设备的端到端加密（业务字节流）
- `[done]` 完成加密 relay 数据面的本地端到端 HTTP 冒烟测试
- `[done]` 为 Agent 常驻模式补更稳的重连与失败恢复
- `[done]` 完成“有效 forward 规则可用、无效规则后台重试且不拖垮 run 进程”的本地冒烟测试
- `[done]` 桌面 GUI 支持读取、编辑、保存 publish / forward 配置
- `[done]` 桌面 GUI 支持把当前 publish 配置同步到控制面
- `[done]` CLI `status` 输出升级为配置加网络快照的派生状态报告
- `[done]` 桌面 GUI 支持刷新状态，并展示设备在线情况、服务同步情况和 forward 规则解析状态
- `[done]` 桌面 GUI 支持启动、停止并展示受管的本地 `minipunch-agent run` 任务状态
- `[done]` `minipunch-agent run` 持续写入本地 runtime state 文件，记录心跳、重启原因和最近事件
- `[done]` 桌面 GUI 接入真实本地 `run` 状态文件，可观测外部 CLI 或桌面受管任务的运行状态
- `[done]` 桌面 GUI 展示最近运行事件、最后一次重启原因和最近心跳时间
- `[done]` 控制面补 direct rendezvous 协调接口，可创建尝试、轮询 pending、提交和查询 UDP 候选地址
- `[done]` Agent/CLI 接入 direct rendezvous 最小工作流，支持 `rendezvous-start / pending / announce / get`
- `[done]` 完成 direct rendezvous 协调链路的本地双设备冒烟测试
- `[done]` Agent 侧创建真实 UDP socket，基于 rendezvous 候选地址发送签名 `hello/ack` 探测包
- `[done]` CLI 新增 `direct-probe`，可在 ready 的 rendezvous 上执行最小打洞探测并选出可达 peer candidate
- `[done]` 完成 direct UDP probe 的本地双设备冒烟测试
- `[done]` CLI 新增 `direct-connect / direct-serve`，可自动完成 start / announce / probe 工作流
- `[done]` 完成 `direct-serve + direct-connect` 的本地自动化冒烟测试
- `[done]` CLI 新增 `direct-tcp-forward / direct-tcp-serve`，可把本地 TCP 流通过 direct UDP channel 代理到远端已发布服务
- `[done]` 完成 direct UDP TCP 代理数据面的本地 HTTP 端到端冒烟测试
- `[done]` 在 agent 侧增加 `auto-forward / auto-serve`，统一 direct / relay 候选链路选择
- `[done]` 为 direct 尝试补超时和失败回落 relay 的状态机
- `[done]` 完成“目标仅提供 relay 服务时，source auto-forward 超时后自动切 relay 并跑通 HTTP”的本地冒烟测试
- `[done]` 把 `auto-forward / auto-serve` 收编进 `run` 的配置驱动模式
- `[done]` 给桌面 GUI 增加 direct/relay 传输策略、UDP bind、candidate type 和 direct wait 配置入口
- `[done]` 完成“两端都跑 run，source 规则配置为 transport=auto，最终选中 direct 并跑通 HTTP”的本地冒烟测试
- `[done]` 给 direct TCP 数据面补最小可靠层：顺序号、累计 ack、stop-and-wait 重传和 close-ack 关闭握手
- `[done]` 完成带最小可靠层的 direct `run` 模式本地 HTTP 端到端冒烟测试
- `[done]` 把 direct TCP 数据面从 stop-and-wait 提升到固定窗口发送，支持多包并发在途、累计 ack 和 go-back-N 式重传
- `[done]` 完成大于 8 个分片的 direct 大 payload 本地端到端冒烟测试
- `[done]` 把 direct TCP 数据面从固定窗口提升到自适应窗口，按 ack 顺畅度加性增大、按超时重传乘性减小
- `[done]` 完成自适应窗口 direct 大 payload 本地回归验证
- `[done]` 为 direct 数据面补 RTT 感知重传超时，按成功 ack 样本收敛 RTO，并在超时后退避
- `[done]` 完成 RTT 感知重传下的 direct 大 payload 本地回归验证
- `[done]` 为 direct 数据面补基于重复 ACK 的快速重传，避免一部分丢包只能等超时
- `[done]` 为快速重传补纯逻辑单元测试，并完成 direct 大 payload 本地回归验证
- `[done]` 为 direct 接收侧补乱序密文缓冲，缺包补到后可连续释放后续已到达分片，减少单点丢包引发的连锁超时
- `[done]` 为接收侧乱序缓冲补纯逻辑单元测试，并完成 direct 大 payload 本地回归验证
- `[done]` 为 direct 发送侧补更成熟的拥塞控制和快速恢复，加入 slow start / congestion avoidance / fast recovery
- `[done]` 为 Reno 风格窗口控制补纯逻辑单元测试，并完成 direct 大 payload 本地回归验证
- `[done]` 为 direct 超时恢复收敛重传范围，改为优先补发最老的超时 outstanding 分片，避免单次超时触发整批重发
- `[done]` 为最老超时分片优先重传补纯逻辑单元测试，并完成 direct 大 payload 本地回归验证
- `[done]` runtime state 补 direct 通道运行指标观测，可记录最近一次活跃 direct channel 的 cwnd / ssthresh / RTO / pending packets / fast recovery
- `[done]` 桌面 GUI 接入 direct 指标观测，展示 direct_metrics
- `[done]` 完成“限速 direct 传输中，source/target runtime JSON 能读到 direct_metrics”的本地验证
- `[done]` runtime state 补 forward/service 级 transport 观测，可区分 configured transport 和 active transport
- `[done]` 桌面 GUI 接入 runtime transport 观测，展示 `Observed Forward Transports` 和 `Observed Service Transports`
- `[done]` 完成“读取 runtime state 文件即可看出某条规则当前跑在 direct、某个 service 当前处于 direct responder”的本地验证
- `[done]` runtime state 补 forward/service 级连接计数，可记录 direct attempt、relay fallback、direct/relay connection 次数和最近对端
- `[done]` 桌面 GUI 接入连接级 runtime 观测，展示 direct/relay 连接次数、最近 transport switch 和最近对端
- `[done]` 完成“同一轮本地联调里分别打 relay 和 direct 流量后，runtime JSON 正确累加连接计数”的本地验证
- `[done]` runtime state 补 auto transport 的分阶段失败观测，可区分 rendezvous start / rendezvous wait / peer lookup / probe / channel open / data plane
- `[done]` 桌面 GUI 接入分阶段失败观测，展示最近一次失败的 transport、stage、时间和错误文本
- `[done]` 完成“目标不在线时，source runtime JSON 正确记录 last_failure_stage=rendezvous_wait”的本地验证
- `[done]` auto transport 在 relay fallback 期间按退避节奏继续重试 direct，不再一直卡在 relay 直到 relay 自己断开
- `[done]` relay 连接在 auto 重新选路时补安全清理，避免丢下残留后台 websocket 任务
- `[done]` 完成“relay-only 目标端下，source 的 direct_attempt_count 持续增长且本地 auto 入口仍可返回 HTTP 200”的本地验证
- `[done]` auto transport 在 relay 仍有活跃连接时延后 direct 重试，等 relay 空闲并经过短暂 idle grace 后再回切
- `[done]` runtime state 补 forward 级 active_connection_count / last_connection_opened_at / last_connection_closed_at
- `[done]` 桌面 GUI 接入 forward 级活跃连接观测，展示 active_conn 和最近连接开闭时间
- `[done]` 完成“活跃 relay 连接期间进入 direct_retry_deferred，连接关闭后恢复 direct 重试”的本地验证
- `[done]` auto transport 在 relay idle 时先 prewarm direct，再切本地 listener 到 direct，缩小 idle handoff 抖动窗口
- `[done]` runtime state 补 `direct_ready` 这类 handoff 过渡状态，能看出 direct 已预热但还没切 listener
- `[done]` 完成“source 先落到 relay，target 之后开启 direct，source 记录 direct_ready -> direct_active 并继续返回 HTTP 200”的本地验证
- `[done]` source 侧 `auto` forward 收敛为单 listener / 统一 ingress，relay/direct handoff 不再重新 bind 本地端口
- `[done]` relay/direct 本地单连接桥接入口抽成可复用接口，供 `auto` listener 直接分发
- `[done]` 完成“relay -> direct handoff 前后，source 的 `127.0.0.1:19110` 保持同一监听 FD，并在切换后继续返回 direct HTTP 200”的本地验证
- `[done]` auto transport 在 relay 仍有活跃连接时也能后台 prewarm direct，不再必须先等 relay 完全 idle 才开始直连准备
- `[done]` 完成“source 在 `active_connection_count=1` 时记录 `prewarming direct rendezvous in parallel`，随后切到 direct 并继续返回 HTTP 200”的本地验证
- `[done]` target 侧 direct service supervisor 不再串行卡住单条 rendezvous；已有 direct attempt 在跑时，新的 attempt 可并发进入 direct probe
- `[done]` runtime state / GUI 为 published service 补 `active_session_count`，可看出当前有多少条 direct session 处于活跃态
- `[done]` target 侧 direct responder 收敛为 shared UDP hub：同一个 `direct_udp_bind_addr` 可并发承接多条 rendezvous，不再依赖同 IP 临时 UDP 端口
- `[done]` 完成“同一时刻连发两条 pending rendezvous，target 两条都通过 `127.0.0.1:41112` 并发进入 direct probe”的本地验证
- `[done]` auto transport 在 relay drain 期间补 per-connection handoff fallback：若新连接在 direct `channel_open` 前失败，可就地回落到仍存活的 relay
- `[done]` 为 handoff fallback 补“pre-bridge 失败时保留原始 socket”的单元测试
- `[done]` `direct-tcp-serve` 改成遇到失败 rendezvous 后继续轮询后续 attempt，不再一条失败就整进程退出
- `[done]` 补 `scripts/smoke_auto_handoff_fallback.sh`，可自动完成 relay-only 起步、slow relay drain、target direct responder 拉起、`direct_handoff_fallback` 命中和最终 sole ingress 验证
- `[done]` 完成“active relay drain 期间 source 命中 `direct_handoff_fallback`，relay drain 完成后回到 `direct_active`”的本地端到端冒烟测试
- `[done]` 为 direct `ChannelAck` 增加少量 SACK 序号，让发送侧可 selectively 清理已乱序到达的 outstanding 分片，减少对尾部分片的重复补发
- `[done]` 为 SACK selective recovery 补纯逻辑单元测试，并完成 handoff smoke 回归验证
- `[done]` 补 `scripts/check_direct_regressions.sh` 作为更轻的 direct 回归入口，默认跑纯逻辑 direct 测试和 `cargo check --workspace`，需要时再通过 `RUN_HANDOFF_SMOKE=1` 串上完整 handoff smoke
- `[done]` 让 direct 恢复逻辑把“单个 ACK 里新出现的 SACK 证据”也计入 fast retransmit 判定，不必总是等 3 个独立重复 ACK 包
- `[done]` 为 SACK 证据累计补纯逻辑单元测试，并通过轻量 direct 回归脚本复验
- `[done]` 把 direct `ChannelAck` 的离散 SACK 序号压缩成连续 range，减少 ACK 包体并为后续 scoreboard 打基础
- `[done]` 为 SACK range 压缩补纯逻辑单元测试，并完成轻量 direct 回归与 handoff smoke 回归验证
- `[done]` sender 侧补显式 SACK scoreboard，统一 ACK range 的合并、累计前移和 selective-acked 查询，给后续更细的 selective retransmit 打基础
- `[done]` 为 sender-side SACK scoreboard 补纯逻辑单元测试，并完成轻量 direct 回归与 handoff smoke 回归验证
- `[done]` duplicate ACK 证据足够多时，sender 可沿 scoreboard 继续选择更后面的 selective hole 做 fast retransmit，不再只盯第一个缺口
- `[done]` 为 selective hole fast retransmit 补纯逻辑单元测试，并完成轻量 direct 回归与 handoff smoke 回归验证
- `[done]` timeout 恢复也开始按 scoreboard 优先补已知 selective hole，而不是优先补推进不了 cumulative ACK 的尾部分片
- `[done]` 为 scoreboard-aware timeout retransmit 补纯逻辑单元测试，并完成轻量 direct 回归与 handoff smoke 回归验证
- `[done]` 把 timeout / fast retransmit / fast recovery 三条路径进一步收敛成统一的 scoreboard-driven loss recovery 选择器
- `[done]` 为统一 loss recovery selector 补纯逻辑单元测试，并完成轻量 direct 回归验证
- `[done]` 补 `scripts/smoke_direct_only.sh`，可自动完成 direct-enabled target、source auto-forward 选中 direct、本地 HTTP / 大 payload 验证
- `[done]` `scripts/check_direct_regressions.sh` 支持按需串上 direct-only smoke，形成 pure tests / direct smoke / handoff smoke 三层回归入口
- `[done]` 控制面不再上送或存储 published service 的真实 `target host/port`，target 侧 relay/direct responder 改为按 `service_id` 在本地配置里解析真实落点
- `[done]` network snapshot 不再向 peers 暴露远端 service 的目标地址和完整 ACL 列表，状态视图改为仅展示 service identity 级信息
- `[done]` relay WebSocket 数据面支持 batched multiplexing，多条 channel envelope 可合并进单个 frame，减少高频小包时的帧开销
- `[done]` 完成 direct-only smoke 与 handoff smoke 回归验证，确认 metadata 收敛和 relay batching 没有打坏主链路
- `[done]` 为 direct channel 增加 keepalive / keepalive-ack / peer idle timeout，减少长空闲 TCP 会话在真实 NAT 下更容易掉线却长期无感的风险
- `[done]` runtime state / GUI 补 direct 的 smoothed RTT 和 keepalive 计数观测，可直接看出直连链路是否在做空闲保活
- `[done]` 为 direct keepalive / peer idle timeout 补纯逻辑单元测试，并通过轻量 direct 回归验证
- `[done]` 桌面 GUI 补系统托盘，支持关窗进托盘、托盘恢复窗口、托盘启停受管 agent 和托盘退出
- `[done]` 桌面 GUI 补自启动管理，可为当前配置写入和移除 macOS launchd / Linux XDG autostart / Windows Startup folder 入口
- `[done]` 桌面 GUI 重构为多 tab 布局，按总览 / 控制面 / 已发布服务 / 转发规则 / 在线设备 / 输出分组，避免单页过度拥挤
- `[done]` 桌面 GUI 为加载配置、保存配置、加入网络、刷新状态、同步服务等操作补统一 loading 和结果提示
- `[done]` 桌面 GUI 启动时忽略上次关闭遗留的 terminal / stale runtime state，避免重启后继续显示旧运行观测
- `[done]` 桌面 GUI 增加在线设备页，列出当前在线设备及其已发布服务，并支持一键生成本地转发草稿
- `[done]` 桌面 GUI 统一把控制面和运行态时间显示格式化为 `YYYY-MM-DD HH:MM:SS`
- `[done]` 桌面 GUI 增加独立“日志”页，集中展示运行观测、链路统计、最近事件和最近一次动作输出
- `[done]` 桌面 GUI 把“输出”页改成“原始配置”页，可直接查看当前配置文件原文
- `[done]` 桌面 GUI 把设备 / 会话 / 已发布 / 转发 / 受管运行 / 运行观测摘要移动到总览页，移除顶部重复摘要
- `[done]` 桌面端已从 `eframe/egui` 迁移到 `Tauri + Vue`：前端改为多 tab WebView 界面，后端改为 Tauri Rust command + tray/autostart 集成
- `[done]` 补桌面端图标资源链路：生成 PNG / ICNS / ICO，并接入 macOS `.app` 包和 Windows 可执行文件资源
- `[done]` Windows 桌面端启用 `windows_subsystem = \"windows\"`，正常启动不再弹出 cmd 窗口
- `[done]` Windows 自启动入口从 Startup folder `.cmd` 改为隐藏式 `.vbs` launcher，并补本地单元测试覆盖
- `[done]` Windows 自启动入口改用专用 `--autostart` 后台启动模式，登录后会自动拉起受管 agent
- `[done]` Windows 自启动在启动受管 agent 前补配置预检；若当前配置未完成入网或缺少身份材料，会直接退出进程而不是残留隐藏后台
- `[done]` 补 macOS DMG、Linux tar.gz、Windows zip 三套打包脚本，并完成 macOS DMG / Linux tar.gz 本地脚本验证
- `[done]` 补 Linux server `.tar.gz` 打包脚本，产出 `minipunch-server` 二进制和 systemd 示例单元
- `[done]` 补 GitHub Actions 手动发版工作流：输入版本号后先校验 tag 递增关系、删除同版本旧 tag/release，再直接创建 GitHub Release，并在 macOS / Windows client 构建前先执行 `npm ci && npm run build`

## 下一批优先任务

- `[next]` 暂无

## 后续任务

- `[later]` direct 数据面的跨 NAT / 长时间 soak / 弱网损伤实机验证
- `[later]` Windows / macOS / Linux 三端桌面 GUI 的人工点按验收
- `[later]` macOS / Windows 安装包的签名、公证和发行级打磨
