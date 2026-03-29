# Task List

用于跟踪 `simple-nat-traversal` 的开发进度。

更新时间：`2026-03-29`

## 状态约定

- `[x]` 已完成
- `[>]` 进行中
- `[ ]` 待做
- `[!]` 风险 / 阻塞 / 需要特别关注

## 目标更新（2026-03-29）

- 新目标：在现有 UDP 打洞组网基础上，支持把远端设备上的 UDP + TCP 服务暴露给其他设备访问，重点覆盖 Windows RDP、SSH 这类 TCP 服务场景。
- 推荐实现假设：继续使用 UDP 完成 NAT 打洞和 peer 会话建立，然后在现有加密 P2P 通道里承载 TCP 字节流。
- 适用前提：TCP 服务可用性依赖双方 UDP 打洞先成功；当前仍不引入 relay，也不做虚拟网卡。

## 当前进度概览

- 当前阶段：`UDP + TCP 主链路、本地重连验证、GUI 重构与收尾已补齐，进入公网联调阶段`
- 当前重点：`准备真实公网联调，记录 UDP 打洞命中情况与 TCP 转发表现`
- 当前边界：
  - 单密码
  - 单逻辑网络
  - UDP 打洞建立加密 P2P 会话
  - 支持远端 UDP + TCP 服务暴露
  - TCP 服务通过现有 UDP P2P 通道转发
  - 不支持中继
  - 不做虚拟网卡
- 当前已知缺口：
  - 真实公网环境下的打洞与 TCP 转发仍待专项实测

## 已完成

### 架构与协议

- [x] 明确单密码、单网络模型，移除多房间设计
- [x] 明确新目标：在 UDP 打洞组网基础上暴露远端 UDP + TCP 服务
- [x] 明确推荐方案：UDP 打洞建链，TCP 通过现有加密 P2P 通道转发
- [x] 搭建 `Linux server + macOS/Windows client` 的 Go 工程结构
- [x] 实现 HTTP 入网接口
- [x] 实现 UDP 注册、候选地址同步与 peer 广播
- [x] 实现多人 mesh 互联模型

### UDP 数据面

- [x] 实现客户端单 UDP socket 复用
- [x] 实现基础 UDP 打洞流程
- [x] 实现端到端加密会话
- [x] 实现本地 UDP `publish`
- [x] 实现远端 UDP `bind`
- [x] 实现请求/响应转发主链路

### 配置与 CLI

- [x] 实现客户端 JSON 配置加载与校验
- [x] 支持 `publish` / `bind` 的 `protocol=udp|tcp`
- [x] 配置向导支持录入 TCP `publish` / `bind`
- [x] 抽取 GUI 可复用的总览数据层
- [x] 支持 `auto_connect` 配置项
- [x] 实现 `-init-config`
- [x] 实现 `-edit-config`
- [x] 实现 `-show-config`
- [x] 实现 `-overview`
- [x] 实现 `-overview-json`
- [x] 实现局部修改配置：
  - [x] `-set-server-url`
  - [x] `-set-password`
  - [x] `-set-device-name`
  - [x] `-set-auto-connect`
  - [x] `-set-udp-listen`
  - [x] `-set-admin-listen`
  - [x] `-upsert-publish`
    - 支持 `name=host:port`
    - 支持 `name=protocol,host:port`
  - [x] `-delete-publish`
  - [x] `-upsert-bind`
    - 支持 `name=peer,service,host:port`
    - 支持 `name=protocol,peer,service,host:port`
  - [x] `-delete-bind`
- [x] 补开机启动管理命令
  - [x] `-autostart-status`
  - [x] `-install-autostart`
  - [x] `-uninstall-autostart`

### 观测与管理

- [x] 实现客户端本地管理接口 `/status`
- [x] 实现 `-status` JSON 状态输出
- [x] 实现 `-peers` 文本视图
- [x] 实现 `-routes` 文本视图
- [x] 实现 `-trace` 联调视图
- [x] 状态快照和服务发现中携带服务协议信息
- [x] 补更明确的重连与失败原因状态摘要
- [x] 给 `-trace` 补充每个 candidate 的耗时与最后成功来源
- [x] 实现服务端在线设备查询接口
- [x] 实现服务端踢设备接口
- [x] 给服务端管理接口补更清晰的错误返回与说明
- [x] 实现客户端侧服务端管理命令：
  - [x] `-network`
  - [x] `-network-json`
  - [x] `-kick-device-name`
  - [x] `-kick-device-id`
- [x] 增加客户端 / 服务端日志级别配置项
- [x] 支持服务端运行中调整日志级别并写回配置
- [x] 支持客户端运行中调整日志级别并写回配置

### 稳定性与测试

- [x] 修复被踢掉设备仍可能通过旧打洞包重新出现的问题
- [x] 补“会话失效 / 被踢 / 网络变化”后的自动重新入网
- [x] 支持客户端优雅离网与快速同名重连
- [x] 增加本地 localhost 端到端集成测试
- [x] 增加多 peer 集成测试
- [x] 增加 peer 离线重连集成测试
- [x] 增加服务端 kick 后重新加入集成测试
- [x] 增加配置向导测试
- [x] 增加配置 patch 测试
- [x] 增加文本状态渲染测试
- [x] 增加 TCP 本地端到端集成测试
- [x] `go build ./...` 通过
- [x] `go test ./...` 通过
- [x] macOS 客户端交叉编译通过
- [x] Windows 客户端交叉编译通过
- [x] Linux 服务端交叉编译通过

## 进行中

### TCP 服务暴露改造

- [x] 协议层增加 `tcp_open` / `tcp_open_result` / `tcp_data` / `tcp_ack` / `tcp_close`
- [x] 客户端 bind 侧 TCP listener 已接入
- [x] 客户端 publish 侧 TCP proxy 已接入
- [x] TCP 字节流分片、ACK、重传、按序重组与关闭控制已接入
- [x] 修复 TCP 服务匹配收口问题，并消除关闭路径中的死锁 / 竞态
- [x] 补 TCP 本地端到端集成测试
- [x] 验证断线重连 / peer 掉线后的 TCP 会话清理与恢复语义
  - 已补并跑通 peer kick 后旧 TCP 会话关闭与重连恢复集成测试
  - 已补并跑通 peer 主动离网 / 重启后的 TCP 会话关闭与恢复集成测试

### 公网联调准备

- [>] 继续准备真实公网联调
  - 先验证 UDP 打洞命中情况
  - 再验证基于 UDP P2P 的 TCP 服务访问

### 控制面与工程化补强

- [x] 检查 GitHub 工作流是否需要补 CI / 构建校验，并按当前项目形态更新
- [x] 增加日志级别控制：
  - 服务端支持命令行实时调整并写入配置
  - 客户端支持 GUI 实时调整并写入配置
- [x] 核实并补测试“客户端启动并与服务端交互通过后自动按配置执行 publish / bind”
- [x] 客户端只接受来自 `server_udp` 的控制面 UDP 包，避免公网环境下被非服务端报文误触发重连或污染 peer 视图
- [x] 收紧 TCP 数据分片尺寸，确保经过 JSON + 加密封装后的 UDP 包仍落在更保守的公网联调预算内

### GUI 桌面端重构

- [x] 增加 GUI locale 检测，优先读取系统 locale 并按中文环境切换文案
- [x] 补 Windows / macOS GUI 图标资源与安装产物图标接入
- [x] 完成 GUI 第一版壳层重构：
  - 左侧导航
  - 中间工作区
  - 右侧情报栏
  - 首页控制台式总览
- [x] 将服务管理、网络设备、诊断、连接设置拆成独立页面
- [x] 将 runtime / 服务 / 日志 / 事件摘要回流到新 GUI 首页
- [x] 收口 GUI 首页摘要与导航语义
  - 已处理未启动客户端时的首页状态表达
  - 已校正 discovered 统计与实际发现列表的一致性
  - 已修正 dashboard 到服务页的操作跳转
- [x] 补齐 GUI 主路径本地化
  - 网络设备页与诊断页改为使用 GUI 本地化渲染
  - 常见后端错误信息接入 GUI 侧本地化映射
  - 设备 ID、状态、表格列名等壳层细节文案统一走翻译表
- [x] 收口 GUI 刷新与状态一致性
  - `Reload Config` 失败时不再误报“已刷新”
  - `Save Config` 后会立刻刷新总览壳层
  - 补首页 / 导航 / 本地化 / 刷新链路回归测试

## 下一步待办

### 高优先级

- [x] 验证 TCP 断线重连 / peer 掉线后的会话清理与恢复语义
- [x] 让 GUI 服务管理页支持选择 `udp/tcp`
- [x] 更新 `README.md` / `docs/USER_GUIDE.md` / `docs/DEPLOYMENT.md` / examples，使其反映 UDP + TCP 目标

### 中优先级

- [ ] 做真实公网联调并记录结果
  - 输出内容：网络环境、UDP 打洞是否成功、TCP 转发是否成功、命中 candidate、失败原因
- [x] 增加 TCP 场景的状态 / 诊断信息
- [x] 增加正式版本号与发布产物命名约定
- [x] 收口 GUI 首页 idle / stopped 语义，避免把正常未启动态显示成异常
- [x] 校正 GUI 首页 discovered 摘要与服务发现列表保持一致

### 低优先级

- [x] 补一个更简洁的使用手册
- [x] 补一个最小 PowerShell / shell 启动脚本
- [x] 补一组常见 TCP 场景示例配置
  - Windows RDP `3389`
  - SSH `22`

## 风险与注意事项

- [!] 双方若处于严格 NAT / 对称 NAT / 某些企业网络，UDP 打洞可能失败；TCP 能力也会随之不可用
- [!] 当前 TCP 方案本质是“在 UDP P2P 通道里承载可靠字节流”，目标优先覆盖 RDP / SSH 这类交互式服务，不等于通用高性能隧道
- [!] 当前密码保存在配置文件中，属于“个人使用可接受”的方案，但仍建议使用强密码
- [!] 当前已验证的是本地集成测试，不等于真实公网环境全部通过
- [!] 这个项目是 UDP overlay，不是透明虚拟局域网，依赖广播发现的程序不一定可直接使用
- [!] 日志级别运行时调整已引入新的本地 / 服务端管理入口；服务端在未配置 `admin_password` 时只允许 loopback 调整

## 验收清单

### 当前阶段验收

- [x] 两台设备可通过同一密码加入同一网络
- [x] 两台设备可建立 P2P UDP 通道
- [x] 一台设备发布本地 UDP 服务，另一台设备可通过本地 bind 端口访问
- [x] 一台设备发布本地 TCP 服务，另一台设备可通过本地 bind 端口访问
- [x] 可查看 peer / route / trace / network 状态
- [x] 可从服务端踢掉指定设备

### 发布前验收

- [x] `go build ./...` 通过
- [x] `go test ./...` 通过
- [x] GitHub 工作流能覆盖日常 CI 校验与正式打包
- [x] GUI 客户端可配置 UDP / TCP 服务
- [x] GUI 客户端可实时调整日志级别并持久化
- [x] 服务端可通过命令行实时调整日志级别并持久化
- [ ] 真实公网两端 UDP 打洞实测通过
- [ ] 基于 UDP P2P 的 TCP 服务转发实测通过
- [x] 断线与重入网行为符合预期
- [x] 文档足够支撑独立部署和使用

## 更新规则

- 每次需求目标调整，先更新本文档里的“目标更新 / 当前边界 / 待办”，再动手改代码
- 每次完成一个可验证功能，就把对应任务从 `[ ]` 改成 `[x]`
- 每次开始一个明确功能，就把它移到“进行中”
- 每次发现真实风险或已知缺口，就补到“风险与注意事项”
- 每次联调后，把结果补到本文档，而不是只留在对话里
