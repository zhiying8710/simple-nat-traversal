# GUI Client

## 目标

`snt-gui` 是面向 macOS / Windows 的原生桌面客户端，使用 Wails 封装现有客户端能力，而不是重新实现一套网络协议。

当前实现采用：

- `Wails App + 原生 WebView 窗口 + 单页仪表盘 UI`
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
  - 结构化 `publish` / `bind`
  - `publish.protocol` / `bind.protocol` 可直接在 GUI 中选择 `udp/tcp`
- 服务管理：
  - 通过 GUI 表单新增/修改/删除本地 `publish` / `bind`
  - 在服务编辑页直接选择 `udp/tcp`
  - 自动发现其他在线设备发布的服务
  - 对发现到的远端服务执行“一键 bind 到本机随机端口”，并保留远端服务的协议信息
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
- [internal/wailsapp/app.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/wailsapp/app.go)
  Wails backend 绑定与桌面壳
- [internal/wailsapp/frontend/src/main.ts](/Users/zhiying8710/wk/simple-nat-traversal/internal/wailsapp/frontend/src/main.ts)
  Wails 前端入口与页面交互
- [internal/wailsapp/frontend/src/types.ts](/Users/zhiying8710/wk/simple-nat-traversal/internal/wailsapp/frontend/src/types.ts)
  前后端共享的数据结构定义
- [internal/desktopapp/service.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/desktopapp/service.go)
  GUI 无关的应用服务层，负责配置合并、启停、刷新、服务管理和网络动作
- [internal/control/overview.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/control/overview.go)
  GUI 总览数据层
- [internal/control/runtime_manager.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/control/runtime_manager.go)
  GUI 内部客户端启停管理
- [internal/autostart/autostart.go](/Users/zhiying8710/wk/simple-nat-traversal/internal/autostart/autostart.go)
  macOS / Windows 开机启动

## 架构

`cmd/snt-gui` 只负责启动日志、解析配置路径和运行时管理器，然后把窗口交给 `internal/wailsapp`。

`internal/wailsapp` 负责：

- 创建原生 WebView 窗口
- 绑定 frontend 与 `desktopapp.Service`
- 通过 embed 打包 `frontend/dist` 静态资源

`internal/desktopapp` 负责：

- 维护连接配置、服务配置和设备管理动作
- 调用 `RuntimeManager` 启停客户端
- 通过 `control.LoadOverview` 聚合总览
- 通过 `client.FetchStatus` / `FetchNetworkDevices` / `KickNetworkDevice` 展示状态和执行网络管理
- 通过 `autostart` 包安装或移除开机启动
- 通过在线设备服务列表生成远端服务发现和一键 bind

前端工程位于 `internal/wailsapp/frontend`，当前使用 `TypeScript + Vite`：

- `src/main.ts` 负责页面状态、表单同步和 Wails backend 调用
- `src/backend.ts` 负责兼容不同 Wails 绑定命名空间
- `src/types.ts` 负责约束前后端交互数据
- `src/style.css` 负责 GUI 样式

如果修改了前端源码，需要在仓库根目录执行：

```bash
cd internal/wailsapp/frontend
npm install
npm run build
```

构建产物会输出到 `internal/wailsapp/frontend/dist`。正式打包建议直接执行：

```bash
bash ./scripts/build-release.sh <version>
```

如果需要在 macOS 上直接从源码构建 GUI，需要执行：

```bash
sdkroot="$(xcrun --sdk macosx --show-sdk-path)"
SDKROOT="$sdkroot" CGO_ENABLED=1 CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=10.13" go build -tags production ./cmd/snt-gui
```

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
- 如果后续需要更复杂的桌面交互，可继续在 `internal/wailsapp` 和前端页面上迭代，而不用改底层网络逻辑
