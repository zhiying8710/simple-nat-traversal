# GUI Client

## 目标

`snt-gui` 是面向 macOS / Windows 的原生桌面客户端，使用 Fyne 封装现有客户端能力，而不是重新实现一套网络协议。

当前实现采用：

- `Fyne App + 原生窗口 + 分组 Tab UI`
- GUI 内部直接调用已有 Go 包完成配置保存、客户端启动/停止、状态获取、网络管理、开机启动管理
- 不再暴露 localhost HTTP API，也不再依赖浏览器作为壳
- 支持在没有现成 `client.json` 的情况下直接启动 GUI，首次配置后再保存

## 为什么这样做

- 更贴近“桌面客户端”的实际使用方式
- 现有 `client/config/autostart/control` 包可以直接复用
- GUI 默认使用用户配置目录下的 `client.json`，CLI 仍可显式指定同一份配置
- 对密码、身份私钥等敏感数据的暴露面更小，不需要再把配置下发给浏览器 JS

## 当前能力

- 总览页：
  - 配置状态
  - 运行状态
  - 网络状态
  - 开机启动状态
- 配置编辑：
  - `server_url`
  - `allow_insecure_http`
  - `password`（留空表示保持已保存值，勾选清空才会删除）
  - `admin_password`（留空表示保持已保存值，勾选清空才会删除）
  - `device_name`（首次无配置时会按“系统版本 + 用户名 + 6 位随机后缀”自动生成）
  - `auto_connect`
  - `udp_listen`
  - `admin_listen`
  - 结构化 `publish`
  - 结构化 `bind`
- 服务管理：
  - 通过 GUI 表单新增/修改/删除本地 `publish`
  - 自动发现其他在线设备发布的服务
  - 对发现到的远端服务执行“一键 bind 到本机随机端口”
- 运行控制：
  - 启动客户端
  - 停止客户端
- 状态查看：
  - 原始 runtime/status 快照
  - peers
  - routes
  - trace candidate stats
  - recent events
  - network device list
  - logs
- 系统集成：
  - 安装开机启动
  - 移除开机启动
  - 按 `device_name` 或 `device_id` 踢设备
- 界面体验：
  - 中文优先的本地化文案
  - 按“连接 / 服务 / 网络 / 诊断”分组组织功能

## 工程结构

- [cmd/snt-gui/main.go](/Users/zhiying8710/wk/simple-nat-traversal/cmd/snt-gui/main.go)
  GUI 入口
- [internal/fyneapp/app.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/fyneapp/app.go)
  Fyne 窗口、表单、Tab 和客户端能力封装
- [internal/control/overview.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/control/overview.go)
  GUI 总览数据层
- [internal/control/runtime_manager.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/control/runtime_manager.go)
  GUI 内部客户端启停管理
- [internal/autostart/autostart.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/autostart/autostart.go)
  macOS / Windows 开机启动

## 架构

`cmd/snt-gui` 只负责启动日志、解析配置路径和运行时管理器，然后把窗口交给 `internal/fyneapp`。

`internal/fyneapp` 负责：

- 创建原生窗口
- 维护连接配置、服务配置和设备管理表单
- 调用 `RuntimeManager` 启停客户端
- 通过 `control.LoadOverview` 聚合总览
- 通过 `client.FetchStatus` / `FetchNetworkDevices` / `KickNetworkDevice` 展示状态和执行网络管理
- 通过 `autostart` 包安装或移除开机启动
- 通过在线设备服务列表生成远端服务发现和一键 bind

## auto_connect 行为

- `auto_connect=true` 时：
  - GUI 启动后会自动尝试启动内置客户端
  - 如果 GUI 自身被设置为开机启动，那么登录后会自动建联
- `auto_connect=false` 时：
  - GUI 只打开原生窗口，不自动连网

## 分发形态

- Windows 发布为安装器 `setup.exe`
- macOS 发布为 `dmg`
- CI 会同时构建 macOS / Windows 的多架构包，避免不同 CPU 设备无法运行

## 后续可扩展方向

- 增加 GUI 的“应用内通知”和“错误高亮”
- 增加导入/导出配置
- 如果后续需要更复杂的桌面交互，可继续在 `internal/fyneapp` 上迭代，而不用改底层网络逻辑
