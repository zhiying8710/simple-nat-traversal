# User Guide

## 日常使用流程

1. 启动 [snt-gui](/Users/zhiying8710/wk/simple-nat-traversal/cmd/snt-gui/main.go)
   它会直接打开原生桌面窗口，不再走浏览器页
2. 在“配置”页填好：
   - `server_url`
   - `password`
   - `admin_password`（如果要查看 network / 踢设备）
   - 如果联调阶段要直连公网 `http://...`，再打开 `allow_insecure_http`
   - `device_name`
   - `publish`
   - `bind`
3. 保存配置
4. 点击“启动客户端”
5. 在总览 / peers / trace / network 中确认状态

## 配置建议

- `device_name` 保持稳定且唯一
- `password` 至少 16 位
- `admin_password` 和 `password` 分开设置
- `admin_listen` 保持在 `127.0.0.1`
- GUI 里密码框留空表示“不修改已保存值”
- 如果希望登录后自动连网，开启 `auto_connect`

## 常见动作

### 发布本地 UDP 服务

例如把本机 `127.0.0.1:19132` 发布成 `game`。

### 绑定远端 UDP 服务

例如把 `winpc` 的 `game` 映射到本机 `127.0.0.1:29132`。

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

## CLI 备用命令

如果 GUI 没开，也可以直接用 CLI：

```bash
./snt -config ./client.json -overview
./snt -config ./client.json -peers
./snt -config ./client.json -routes
./snt -config ./client.json -trace
./snt -config ./client.json -network
```

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
