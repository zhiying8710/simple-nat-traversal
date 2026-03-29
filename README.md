# simple-nat-traversal

一个基于 `UDP P2P 打洞` 组网，并把远端 `UDP + TCP` 服务暴露给其他设备访问的最小工具。

- 服务端只维护一个逻辑网络，校验一个配置里的密码，并分发在线设备的候选地址。
- 客户端在同一个 UDP socket 上完成注册、打洞和后续业务数据传输。
- 只要客户端配置里的密码和服务端一致，多台设备就能加入同一个多人 mesh 网络。
- UDP 服务直接走现有 P2P 数据面转发。
- TCP 服务先通过 UDP 完成 NAT 打洞和 peer 会话建立，再在这条加密 P2P 通道里承载 TCP 字节流。
- 重点覆盖 Windows RDP、SSH 这类 TCP 服务场景。
- 不支持中继，不做虚拟网卡。

## 当前实现

- `cmd/snt-server`: 会合服务器，提供 HTTP 控制面和 UDP 会合面。
- `cmd/snt`: macOS / Windows / Linux 都可编译运行的客户端。
- `cmd/snt-gui`: 基于 Fyne 的原生桌面 GUI，优先面向 macOS / Windows。
- 服务端配置里保存一个网络密码，客户端只要密码正确就能加入同一个网络。
- 服务端管理接口使用独立的 `admin_password`；客户端只有配置了相同的 `admin_password` 才能查看在线设备或踢设备。
- P2P 会话使用该密码派生出的网络密钥做握手认证，再用临时 X25519 + AES-GCM 加密业务包。
- 客户端首次保存配置或首次运行时会生成并持久化设备身份密钥，用于跨重启保持稳定设备身份。
- 服务端会把 `device_name -> identity_public` 归属持久化到状态文件，默认写到与服务端配置同目录的 `*.state.json`，用于阻止离线设备名被其他身份接管。
- 一个客户端可以发布多个本地 UDP / TCP 服务，也可以把多个远端 UDP / TCP 服务绑定到本地端口。
- TCP 数据面包含 `open` / `data` / `ack` / `close` 控制，按分片、确认、重传、按序重组的方式在 UDP P2P 通道上传输字节流。
- 客户端在会话失效、被服务端踢掉或设备长时间未注册后，会自动重新入网并重建 P2P 会话。
- `auto_connect=true` 时，GUI 窗口启动后会自动尝试拉起客户端并自动建联。
- GUI 支持在没有现成 `client.json` 的情况下直接启动，首次保存时会写入系统用户配置目录。
- GUI 内置结构化的服务管理界面，可以直接新增/修改 `publish` / `bind`，并为每条服务选择 `udp/tcp` 协议。
- GUI 发现其他设备的已发布服务后，也可以一键 `bind` 到本机随机端口，并保留远端服务的协议信息。

## 明确边界

- 没有 relay。双方 NAT 太严格时，会失败。
- TCP 能力依赖 UDP 打洞先成功；如果双方网络让 UDP 打洞失败，TCP 转发也不可用。
- 这是 UDP overlay，不是系统级 LAN 模拟器。依赖广播/组播发现的程序不一定能直接用。
- TCP 方案的目标优先是 RDP / SSH 这类交互式服务，不是通用高吞吐隧道。
- 密码直接保存在客户端 JSON 配置里，配合 `-init-config` / `-edit-config` 维护。
- 客户端可选开启本地只读管理端口，用来查看实时状态。
- 建议网络密码至少 16 位。

## 配置

服务端示例见 [examples/server.json](/Users/zhiying8710/wk/simple-nat-traversal/examples/server.json)。

客户端示例见：

- [examples/client-linux.json](/Users/zhiying8710/wk/simple-nat-traversal/examples/client-linux.json)
- [examples/client-macos.json](/Users/zhiying8710/wk/simple-nat-traversal/examples/client-macos.json)
- [examples/client-windows.json](/Users/zhiying8710/wk/simple-nat-traversal/examples/client-windows.json)

注意：

- `public_udp_addr` 必须填 VPS 的真实公网 `IP:port`。
- 默认建议把 `server_url` 写成 `https://...`。
- 如果联调阶段必须直连公网 `http://...`，需要显式打开 `allow_insecure_http=true`；`localhost` / `127.0.0.1` / `::1` 的本地调试例外。
- `password` 需要在服务端和所有客户端配置里保持一致。
- `admin_password` 应单独设置；只有需要查看 `network` / 执行 `kick` 的客户端才需要配置它。
- `device_name` 在同一个网络里必须唯一，因为 `binds.*.peer` 通过名字找目标设备。
- `admin_listen` 必须显式绑定到 `127.0.0.1`、`::1` 或 `localhost`。
- `auto_connect=true` 适合 GUI + 开机启动场景。
- `publish.*.protocol` / `binds.*.protocol` 支持 `udp` 或 `tcp`；留空时默认按 `udp` 处理。
- `upsert-publish` 支持：
  - `name=host:port`
  - `name=protocol,host:port`
- `upsert-bind` 支持：
  - `name=peer,service,host:port`
  - `name=protocol,peer,service,host:port`
- `install-autostart` 需要用正式构建出来的 `snt` 二进制执行，不能用 `go run`，否则登录项会指向临时文件。
- `snt-gui` 的开机启动会把 GUI 自己设为登录项；如果 `auto_connect=true`，则登录后自动建联。

## 运行

构建：

```bash
go build ./...
```

说明：

- `cmd/snt` 和 `cmd/snt-server` 仍然适合交叉编译。
- 正式发布会产出：
  - Windows 多架构安装器 `setup.exe`
  - macOS 多架构 `dmg`

初始化客户端配置：

```bash
go run ./cmd/snt -config ./client.json -init-config
```

编辑已有客户端配置：

```bash
go run ./cmd/snt -config ./client.json -edit-config
```

直接修改单个字段或单个映射：

```bash
export SNT_PASSWORD='your-new-password'
export SNT_ADMIN_PASSWORD='your-admin-password'
go run ./cmd/snt -config ./client.json -set-password-env SNT_PASSWORD
go run ./cmd/snt -config ./client.json -set-admin-password-env SNT_ADMIN_PASSWORD
go run ./cmd/snt -config ./client.json -set-allow-insecure-http true
go run ./cmd/snt -config ./client.json -set-auto-connect true
go run ./cmd/snt -config ./client.json -upsert-publish game=127.0.0.1:19132
go run ./cmd/snt -config ./client.json -upsert-publish rdp=tcp,127.0.0.1:3389
go run ./cmd/snt -config ./client.json -upsert-publish ssh=tcp,127.0.0.1:22
go run ./cmd/snt -config ./client.json -upsert-bind win-game=winpc,game,127.0.0.1:29132
go run ./cmd/snt -config ./client.json -upsert-bind win-rdp=tcp,winpc,rdp,127.0.0.1:13389
go run ./cmd/snt -config ./client.json -upsert-bind server-ssh=tcp,server,ssh,127.0.0.1:10022
go run ./cmd/snt -config ./client.json -delete-publish game
go run ./cmd/snt -config ./client.json -delete-bind win-game
```

一个典型的 TCP 场景：

- Windows 机器发布 `rdp=tcp,127.0.0.1:3389`
- 另一台机器绑定 `win-rdp=tcp,winpc,rdp,127.0.0.1:13389`
- 然后直接连本地 `127.0.0.1:13389`，流量会经 UDP P2P 通道转到远端 Windows 的 `3389`

也可以从文件读取密码，避免把秘密写到命令行参数里：

```bash
go run ./cmd/snt -config ./client.json -set-password-file ./password.txt
go run ./cmd/snt -config ./client.json -set-admin-password-file ./admin-password.txt
```

查看当前配置：

```bash
go run ./cmd/snt -config ./client.json -show-config
```

如果你确实需要查看包含密码和身份私钥的原始配置，再显式使用：

```bash
go run ./cmd/snt -config ./client.json -show-config-unsafe
```

查看适合 GUI 复用的总览信息：

```bash
go run ./cmd/snt -config ./client.json -overview
go run ./cmd/snt -config ./client.json -overview-json
```

查看开机启动状态：

```bash
./snt -config ./client.json -autostart-status
```

安装开机启动：

```bash
./snt -config ./client.json -install-autostart
```

移除开机启动：

```bash
./snt -config ./client.json -uninstall-autostart
```

启动服务端：

```bash
go run ./cmd/snt-server -config ./examples/server.json
```

启动客户端：

```bash
go run ./cmd/snt -config ./examples/client-macos.json
go run ./cmd/snt -config ./examples/client-windows.json
```

启动 GUI：

```bash
go run ./cmd/snt-gui
```

查看版本：

```bash
go run ./cmd/snt -version
go run ./cmd/snt-server -version
go run ./cmd/snt-gui -version
```

查看客户端状态：

```bash
go run ./cmd/snt -config ./examples/client-macos.json -status
```

查看更适合联调的文本状态：

```bash
go run ./cmd/snt -config ./examples/client-macos.json -peers
go run ./cmd/snt -config ./examples/client-macos.json -routes
go run ./cmd/snt -config ./examples/client-macos.json -trace
go run ./cmd/snt -config ./examples/client-macos.json -network
```

说明：

- `-routes` 会显示 `publish` / `bind` 概览，以及当前活动中的 `tcp_bind_streams` / `tcp_publish_proxies`
- `-trace` 除了 candidate 命中信息外，还会显示当前 `tcp_runtime` 和最近的 TCP 打开/关闭/清理事件

或者直接请求本地管理接口：

```bash
curl http://127.0.0.1:19090/status
```

查看服务端当前在线设备：

```bash
go run ./cmd/snt -config ./examples/client-macos.json -network
```

查看服务端当前在线设备的原始 JSON：

```bash
go run ./cmd/snt -config ./examples/client-macos.json -network-json
```

手动踢掉某个设备：

```bash
go run ./cmd/snt -config ./examples/client-macos.json -kick-device-name winpc
go run ./cmd/snt -config ./examples/client-macos.json -kick-device-id dev_xxx
```

## GUI

推荐在 macOS / Windows 上优先使用 GUI：

- 配置编辑更直接
- 可视化查看 peers / routes / trace / network
- 可直接启动 / 停止客户端
- 可直接管理开机启动
- 不再暴露本地浏览器控制面，而是直接提供原生桌面窗口
- GUI 的 `password` / `admin_password` 输入框默认不会回显已保存值；留空表示保持原值，勾选对应清空框才会删除已保存密码
- GUI 的 `publish` / `bind` 表单可直接选择 `udp` 或 `tcp`，适合在桌面端维护 RDP / SSH 这类 TCP 服务映射

GUI 相关文档：

- [docs/GUI_CLIENT.md](/Users/zhiying8710/wk/simple-nat-traversal/docs/GUI_CLIENT.md)
- [docs/DEPLOYMENT.md](/Users/zhiying8710/wk/simple-nat-traversal/docs/DEPLOYMENT.md)
- [docs/USER_GUIDE.md](/Users/zhiying8710/wk/simple-nat-traversal/docs/USER_GUIDE.md)

## 数据流

1. 客户端通过 HTTP 用密码加入默认单网络。
2. 客户端通过 UDP 向服务器注册自己的候选地址和已发布服务。
3. 服务器把当前网络内在线成员同步给所有客户端。
4. 客户端两两发送 `punch_hello`，收到对端有效握手包后建立加密 P2P 会话。
5. UDP 场景下，本地应用把 UDP 包发给 `bind` 监听端口，客户端会把它加密后直接发给远端 peer。
6. 远端 peer 把 UDP 包转给本地 `publish` 的 UDP 服务，并把响应包再经 P2P 会话送回来。
7. TCP 场景下，本地应用连接 `bind` 的 TCP 监听端口，客户端会先发送 `tcp_open`，随后把 TCP 字节流切成分片，经加密 P2P 通道发送给远端 peer。
8. 远端 peer 把这些分片按序写入本地 TCP `publish` 服务，并通过 `tcp_ack` / 重传机制维持可靠传输，直到任一侧关闭连接。

## 发布与脚本

- [scripts/build-release.sh](/Users/zhiying8710/wk/simple-nat-traversal/scripts/build-release.sh)
- [scripts/build-release.ps1](/Users/zhiying8710/wk/simple-nat-traversal/scripts/build-release.ps1)
- [.github/workflows/package-release.yml](/Users/zhiying8710/wk/simple-nat-traversal/.github/workflows/package-release.yml)
- [scripts/run-gui.sh](/Users/zhiying8710/wk/simple-nat-traversal/scripts/run-gui.sh)
- [scripts/run-gui.ps1](/Users/zhiying8710/wk/simple-nat-traversal/scripts/run-gui.ps1)
