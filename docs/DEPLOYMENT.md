# Deployment

## 1. 服务端准备

需要一台有公网 IP 的 Linux VPS。

准备一个服务端配置，例如：

```json
{
  "http_listen": ":8080",
  "udp_listen": ":3479",
  "public_udp_addr": "YOUR_VPS_PUBLIC_IP:3479",
  "password": "replace-with-your-own-strong-password",
  "admin_password": "replace-with-a-separate-admin-password"
}
```

要求：

- `public_udp_addr` 必须填真实公网 `IP:port`
- `password` 需要和所有客户端一致
- `admin_password` 应和客户端里用于管理网络的配置单独对应，不要复用 `password`
- 服务端默认会在配置文件旁边生成一个 `*.state.json` 状态文件，用来持久化设备名和身份公钥的绑定关系；如果需要也可以显式配置 `state_path`
- 建议 HTTP 层通过反向代理挂上 HTTPS

## 2. 构建

可以直接本机构建：

```bash
go build ./...
```

如果要出正式包，建议用 [scripts/build-release.sh](/Users/zhiying8710/wk/simple-nat-traversal/scripts/build-release.sh) 或 [scripts/build-release.ps1](/Users/zhiying8710/wk/simple-nat-traversal/scripts/build-release.ps1)。

说明：

- `snt` / `snt-server` 仍然适合交叉编译。
- 正式发布链路现在会产出：
  - Windows `amd64` / `arm64` `setup.exe`
  - macOS universal `dmg`
  - 常用桌面架构的安装包
- 本地 [scripts/build-release.ps1](/Users/zhiying8710/wk/simple-nat-traversal/scripts/build-release.ps1) 会按当前 Windows 主机架构生成对应安装包：x64 主机打 `amd64`，ARM 主机打 `arm64`。

## 3. 启动服务端

```bash
./snt-server -config ./server.json
```

建议：

- 用 systemd 或 supervisor 常驻
- 打开 HTTP 端口和 UDP 端口
- 确认云厂商安全组同时放行 TCP/UDP

## 4. 客户端准备

CLI 使用时可以先准备 `client.json`：

```json
{
  "server_url": "https://YOUR_DOMAIN_OR_IP",
  "password": "replace-with-your-own-strong-password",
  "admin_password": "replace-with-a-separate-admin-password",
  "device_name": "macbook-air",
  "auto_connect": true,
  "udp_listen": ":0",
  "admin_listen": "127.0.0.1:19090",
  "publish": {
    "game": {
      "protocol": "udp",
      "local": "127.0.0.1:19132"
    },
    "ssh": {
      "protocol": "tcp",
      "local": "127.0.0.1:22"
    }
  },
  "binds": {
    "win-game": {
      "protocol": "udp",
      "peer": "winpc",
      "service": "game",
      "local": "127.0.0.1:29132"
    },
    "win-rdp": {
      "protocol": "tcp",
      "peer": "winpc",
      "service": "rdp",
      "local": "127.0.0.1:13389"
    }
  }
}
```

说明：

- `admin_password` 只用于查看在线设备和踢设备；如果某台客户端不需要这些管理能力，可以留空
- 客户端会在首次保存配置或首次运行时自动生成稳定设备身份并写回配置
- GUI 在首次启动时即使还没有 `client.json` 也可以直接打开，保存后会写入系统用户配置目录
- GUI 首次无配置启动时会自动生成一个尽量不重名的 `device_name`
- `publish.*.protocol` / `binds.*.protocol` 可选 `udp` 或 `tcp`
- 如果联调阶段需要临时直连公网 `http://...`，再显式加上 `"allow_insecure_http": true`；正式环境仍建议走反代后的 `https://...`

## 5. 启动 GUI

macOS / Windows 推荐直接启动 GUI：

```bash
./snt-gui
```

启动后会直接打开原生窗口，不再依赖浏览器。
GUI 默认使用系统用户配置目录下的 `client.json`。
如果 `auto_connect=true`，GUI 打开后会自动尝试拉起客户端。
GUI 的服务管理页可以直接为每条 `publish` / `bind` 选择 `udp/tcp`。

## 6. 设置登录后自动启动

注意：需要使用正式构建好的二进制，不能用 `go run`。

GUI 自启：

```bash
./snt-gui -config ./client.json
```

然后在 GUI 里点击“安装开机启动”。

或者命令行：

```bash
./snt -config ./client.json -install-autostart
```

如果 `auto_connect=true`，GUI 登录后会自动建联。

## 7. Windows 注意事项

- 首次运行可能会弹系统防火墙提示
- 需要允许客户端二进制访问网络
- 如果本地 UDP 服务绑定到特定端口，也要确认本机没有其他程序占用

## 8. 故障排查

优先看：

- GUI 总览页
- GUI 的 Trace 区域
- GUI 的 Logs 区域

CLI 也可以辅助查看：

```bash
./snt -config ./client.json -overview
./snt -config ./client.json -trace
./snt -config ./client.json -network
./snt -config ./client.json -routes
```

建议：

- UDP 打洞是否命中，优先看 `-trace`
- TCP 绑定是否已经建链、是否被清理，优先看 `-routes` 里的 `tcp_bind_streams` / `tcp_publish_proxies`
- 如果 TCP 会话异常关闭，再看 `-trace` 最近事件里的 `tcp_open_failed`、`tcp_bind_remote_close`、`tcp_transport_reset` 等原因

如果双方网络非常严格，纯 UDP 打洞可能仍然失败，这属于当前产品边界。
