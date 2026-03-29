# User Guide

## 日常使用流程

1. 启动 [snt-gui](/Users/zhiying8710/wk/simple-nat-traversal/cmd/snt-gui/main.go)
   它会直接打开原生桌面窗口，不再走浏览器页
2. 如果这是第一次启动，没有现成 `client.json` 也没关系。
   GUI 会先用默认值打开，并自动生成一个候选 `device_name`
3. 在“连接”页填好：
   - `server_url`
   - `password`
   - `admin_password`（如果要查看 network / 踢设备）
   - 如果联调阶段要直连公网 `http://...`，再打开 `allow_insecure_http`
   - `device_name`
4. 保存配置
5. 点击“启动客户端”
6. 在总览 / network / trace / logs 中确认状态

## 配置建议

- `device_name` 保持稳定且唯一
- 没有特殊原因时，首次自动生成的 `device_name` 可以直接沿用
- `password` 至少 16 位
- `admin_password` 和 `password` 分开设置
- `admin_listen` 保持在 `127.0.0.1`
- GUI 里密码框留空表示“不修改已保存值”
- 如果希望登录后自动连网，开启 `auto_connect`

## 常见动作

### 发布本地服务

进入 GUI 的“服务”页，在“我发布的服务”区域先选择协议，再填写服务名和本地服务地址。

- UDP 例子：把本机 `127.0.0.1:19132` 发布成 `game`
- TCP 例子：把本机 `127.0.0.1:3389` 发布成 `rdp`

### 绑定远端服务

推荐直接在“发现到的远端服务”区域选择在线设备发布出来的服务，然后点击“一键绑定到本机随机端口”。
如果远端服务是 TCP，列表里会显示成类似 `rdp/tcp`，GUI 会自动按 TCP 创建 bind。

如果需要手工指定本地监听地址，也可以在“我绑定的服务”区域编辑，并手工选择 `udp/tcp`。

- UDP 例子：绑定 `winpc / game` 到 `127.0.0.1:29132`
- TCP 例子：绑定 `winpc / rdp/tcp` 到 `127.0.0.1:13389`

### 看当前在线设备

在 GUI 的“网络设备”区域查看。

### 踢掉异常设备

在 GUI 的“网络设备”区域按 `device_name` 或 `device_id` 执行。

如果 GUI 没开，也可以用：

```bash
./snt -config ./client.json -kick-device-name winpc
./snt -config ./client.json -kick-device-id dev_xxx
```

### 看打洞是否成功

在 GUI 的：

- Peer 与路由
- Trace
- Logs

这三个区域联合判断。

如果是 TCP 场景，再重点看：

- `routes` 里的 `tcp_bind_streams` / `tcp_publish_proxies`
- `trace` 里的 `tcp_runtime`
- 最近事件里是否出现 `tcp_open_failed`、`tcp_bind_remote_close`、`tcp_peer_cleanup` 这类条目

## 安装包说明

- Windows 建议使用安装器 `setup.exe`
- macOS 建议使用 `dmg`
- GUI 默认使用系统用户配置目录下的 `client.json`，不依赖启动时所在目录

## CLI 备用命令

如果 GUI 没开，也可以直接用 CLI：

```bash
./snt -config ./client.json -overview
./snt -config ./client.json -peers
./snt -config ./client.json -routes
./snt -config ./client.json -trace
./snt -config ./client.json -network
```

其中：

- `-routes` 更适合看当前 TCP 会话是否已经建立
- `-trace` 更适合看打洞候选命中、重连原因和最近的 TCP 关闭/恢复事件

## 什么时候用 GUI

推荐默认用 GUI：

- 配配置更快
- 看状态更直观
- 管理开机启动更方便
- 不需要再暴露 localhost 浏览器控制面

CLI 更适合：

- 远程 shell
- 调试
- 脚本化
