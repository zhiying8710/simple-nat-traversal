# Task List

用于跟踪 `simple-nat-traversal` 的开发进度。

更新时间：`2026-03-28`

## 状态约定

- `[x]` 已完成
- `[>]` 进行中
- `[ ]` 待做
- `[!]` 风险 / 阻塞 / 需要特别关注

## 当前进度概览

- 当前阶段：`除公网联调外的开发任务已完成`
- 当前重点：`等待 GUI 之后的真实公网联调`
- 当前边界：
  - 单密码
  - 单逻辑网络
  - 纯 UDP 打洞
  - 纯 UDP 端口映射
  - 不支持中继
  - 不支持 TCP 转发

## 已完成

### 架构与协议

- [x] 明确单密码、单网络模型，移除多房间设计
- [x] 明确产品边界：仅 UDP，不做 relay，不做 TCP 转发
- [x] 搭建 `Linux server + macOS/Windows client` 的 Go 工程结构
- [x] 实现 HTTP 入网接口
- [x] 实现 UDP 注册、候选地址同步与 peer 广播
- [x] 实现多人 mesh 互联模型

### P2P 与数据面

- [x] 实现客户端单 UDP socket 复用
- [x] 实现基础 UDP 打洞流程
- [x] 实现端到端加密会话
- [x] 实现本地 UDP `publish`
- [x] 实现远端 UDP `bind`
- [x] 实现请求/响应转发主链路

### 配置与 CLI

- [x] 实现客户端 JSON 配置加载与校验
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
  - [x] `-delete-publish`
  - [x] `-upsert-bind`
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
- [x] `go build ./...` 通过
- [x] `go test ./...` 通过
- [x] macOS 客户端交叉编译通过
- [x] Windows 客户端交叉编译通过
- [x] Linux 服务端交叉编译通过

## 进行中

- [>] 等待公网联调
  - 目标：在 GUI MVP 基础上做真实网络环境验证
  - 当前情况：非公网联调任务已经全部完成

## 下一步待办

### 高优先级

- [x] 搭建 macOS / Windows GUI 客户端 MVP
  - 目标：可视化操作现有 CLI 能力，不另造一套协议
- [x] 明确 GUI 技术路线与工程结构
  - 要求：优先复用现有 Go 代码与配置逻辑
- [x] 实现 GUI 主总览页
  - 展示配置状态、运行状态、网络状态、开机启动状态
- [x] GUI 支持可视化配置编辑
  - 包括 `server_url`、`password`、`device_name`、`publish`、`bind`
- [x] GUI 支持可视化操作当前 CLI 命令
  - 包括启动客户端、查看 `peers` / `routes` / `trace` / `network`
- [x] GUI 支持设置开机启动
  - macOS：登录后自动拉起
  - Windows：登录后自动拉起
- [x] GUI 支持配置完整后自动建联
  - 即开机后自动启动客户端并自动连入当前网络
- [x] 梳理一版最小部署说明
  - 包括 VPS 部署、Windows 防火墙提示、客户端启动方式
- [x] 完成 GUI 最终验证与目标平台交叉编译复核

### 中优先级

- [ ] 做真实公网联调并记录结果
  - 前置条件：GUI 客户端 MVP 完成
  - 输出内容：网络环境、是否成功、命中 candidate、失败原因
- [x] 增加更多集成测试场景
  - 多 peer
  - peer 离线重连
  - 服务端 kick 后重新加入
- [x] 增加正式版本号与发布产物命名约定

### 低优先级

- [x] 补一个更简洁的使用手册
- [x] 补一个最小 PowerShell / shell 启动脚本

## 风险与注意事项

- [!] 双方若处于严格 NAT / 对称 NAT / 某些企业网络，纯打洞可能失败
- [!] 当前密码保存在配置文件中，属于“个人使用可接受”的方案，但仍建议使用强密码
- [!] 当前已验证的是本地集成测试，不等于真实公网环境全部通过
- [!] 这个项目是 UDP overlay，不是透明虚拟局域网，依赖广播发现的程序不一定可直接使用

## 验收清单

### MVP 验收

- [x] 两台设备可通过同一密码加入同一网络
- [x] 两台设备可建立 P2P UDP 通道
- [x] 一台设备发布本地 UDP 服务，另一台设备可通过本地 bind 端口访问
- [x] 可查看 peer / route / trace / network 状态
- [x] 可从服务端踢掉指定设备

### 发布前验收

- [x] GUI 客户端可完成主要常用操作
- [x] GUI 客户端开机启动可用
- [ ] 真实公网两端实测通过
- [x] 断线与重入网行为符合预期
- [x] 文档足够支撑独立部署和使用

## 更新规则

- 每次完成一个可验证功能，就把对应任务从 `[ ]` 改成 `[x]`
- 每次开始一个明确功能，就把它移到“进行中”
- 每次发现真实风险或已知缺口，就补到“风险与注意事项”
- 每次联调后，把结果补到本文档，而不是只留在对话里
