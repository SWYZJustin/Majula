# Majula 分布式通信中间件 / Distributed Communication Middleware

Majula 是一个基于 Go 语言开发的分布式通信中间件，支持高性能的节点间消息路由、RPC 调用、主题订阅发布、内网穿透（FRP）、Nginx 反向代理等功能。适用于微服务通信、分布式系统、内网穿透、消息推送等多种场景。

Majula is a distributed communication middleware written in Go, supporting high-performance message routing, RPC, topic pub/sub, FRP tunneling, Nginx reverse proxy, and more. Ideal for microservices, distributed systems, NAT traversal, and messaging scenarios.

---

## 🌟 主要功能 / Features

- **分布式节点管理** / Distributed node management
- **高性能消息路由** / High-performance message routing
- **RPC 远程调用** / RPC remote invocation
- **WebSocket/HTTP 接口** / WebSocket & HTTP APIs
- **FRP 内网穿透** / Built-in FRP tunneling
- **Nginx 反向代理** / Dynamic Nginx reverse proxy
- **可扩展架构** / Extensible architecture

---

## 🚀 快速启动本地节点 / Quick Start

### 1. 安装依赖 / Install dependencies
```bash
go mod tidy
```

### 2. 启动本地节点（示例脚本）/ Start local nodes (example)
```bash
nohup go run MajulaNodeFromYaml.go MajulaNode1.yaml &
nohup go run MajulaNodeFromYaml.go MajulaNode2.yaml &
```
> 可根据 `MajulaNodeTemplate.yaml` 自定义节点配置。
> You can customize node config via `MajulaNodeTemplate.yaml`.

### 3. 连接节点并测试 / Connect & test
- 使用 WebSocket 客户端或 curl/Postman 进行 HTTP 测试
- Use WebSocket client or curl/Postman for HTTP testing
- 也可用 `api/MajulaClient.go` 作为 SDK 进行二次开发
- You can use `api/MajulaClient.go` as SDK for development

---

## 📡 HTTP 接口说明 / HTTP API

所有接口前缀为 `/majula`，支持 GET/POST。
All endpoints are under `/majula`, support GET/POST.

| 路径 Path   | 说明 Description      | 主要参数 Main Params |
|-------------|----------------------|---------------------|
| /ws         | WebSocket 连接       | target（可选）      |
| /h          | HTTP 消息收发        | see below           |
| /sub        | 订阅主题 Subscribe   | topic               |
| /pub        | 发布主题消息 Publish | topic, args         |
| /rpc        | RPC 调用             | fun, args, ...      |
| /send       | 私有消息 Private Msg | target_node, ...    |
| /list_rpc   | 列出RPC服务 List RPC |                     |
| /map        | Nginx代理管理        | see below           |
| /frp        | FRP管理              | see below           |
| /upload     | 文件上传 Upload      |                     |
| /download   | 文件下载 Download    |                     |

**示例 Example:**
```bash
curl -X POST http://localhost:8080/majula/sub -d '{"topic":"test"}'
curl -X POST http://localhost:8080/majula/pub -d '{"topic":"test","args":{"msg":"hello"}}'
curl -X POST http://localhost:8080/majula/rpc -d '{"fun":"add","args":{"a":1,"b":2}}'
```

---

## 🔗 WebSocket API (推荐/Recommended)

### 连接方式 / How to connect
```
ws://localhost:8080/majula/ws/{client_id}
```

### 消息包格式 / Message Format

所有 WebSocket 消息均为 JSON 格式，结构如下：
All WebSocket messages are JSON objects, as below:

```json
{
  "method": "SUBSCRIBE|PUBLISH|RPC|SEND|REGISTER_RPC|UNREGISTER_RPC|QUIT|...",
  "topic": "test",           // 主题 topic (optional)
  "fun": "add",              // RPC方法名 function name (optional)
  "args": {"a":1,"b":2},    // 参数 params (optional)
  "invokeid": 123,           // 调用ID invoke id (optional)
  "result": null             // 返回结果 result (server response)
}
```

### 常用 method 说明 / Common methods
- `SUBSCRIBE`：订阅主题 / Subscribe topic
- `UNSUBSCRIBE`：取消订阅 / Unsubscribe topic
- `PUBLISH`：发布主题消息 / Publish topic message
- `RPC`：调用远程RPC / Call remote RPC
- `REGISTER_RPC`：注册本地RPC服务 / Register local RPC
- `UNREGISTER_RPC`：注销本地RPC服务 / Unregister local RPC
- `SEND`：发送私有消息 / Send private message
- `QUIT`：主动断开连接 / Quit

### 服务端推送消息示例 / Server push example
```json
{
  "method": "SUB_RESULT",
  "topic": "test",
  "args": {"msg":"hello"}
}
```

---

## 🧩 WebSocket 客户端 API（Go SDK）/ WebSocket Client API (Go SDK)

推荐使用 `api/apis.go` 中的 `Client` 封装，简化 WebSocket 通信和 API 调用。
We recommend using the `Client` struct in `api/apis.go` for easy WebSocket API usage.

### 主要方法 / Main Methods

- `NewClient(addr, entity)`：创建并连接 WebSocket 客户端 / Create and connect client
- `CallRpc(fun, args, targetNode, provider, timeout)`：远程 RPC 调用 / Remote RPC call
- `RegisterRpc(fun, handler, meta)`：注册本地 RPC / Register local RPC
- `CallRpcAsync(...)`：异步 RPC 调用 / Async RPC call
- `Subscribe(topic, handler)`：订阅主题 / Subscribe topic
- `Unsubscribe(topic)`：取消订阅 / Unsubscribe
- `Publish(topic, args)`：发布消息 / Publish message
- `OnPrivate(handler)`：设置私有消息回调 / Set private message handler
- `SendPrivate(targetNode, targetClient, payload)`：发送私有消息 / Send private message
- `RegisterFRP(...)`、`RegisterNginxFRP(...)` 等：FRP/Nginx 相关操作
- `UploadFile(...)`、`DownloadFile(...)`：文件传输
- `Quit()`：关闭连接 / Quit

#### 代码示例 / Example
```go
client := api.NewClient("ws://localhost:8080", "my-client")
client.Subscribe("test", func(topic string, args map[string]interface{}) {
    fmt.Println("收到消息/Received:", topic, args)
})
client.Publish("test", map[string]interface{}{"msg": "hello"})
res, ok := client.CallRpc("add", map[string]interface{}{"a":1, "b":2}, "targetNode", "default", time.Second)
if ok {
    fmt.Println("RPC结果/RPC result:", res)
}
client.Quit()
```

---

## 🛠️ 进阶功能 / Advanced Features

- **FRP 内网穿透 / FRP tunneling**：通过 `/majula/frp` 或 SDK 注册/启动 FRP 隧道，实现节点间穿透通信。
- **Nginx 反向代理 / Nginx reverse proxy**：通过 `/majula/map` 或 SDK 动态注册本地服务到远程节点，实现 HTTP 服务暴露。
- **文件传输 / File transfer**：支持节点间文件上传/下载。

---

## ⚙️ 依赖与构建 / Dependencies & Build

- Go 1.18+
- 依赖见 `go.mod` / See `go.mod`
- 推荐使用 Linux/Mac/WSL 环境运行 / Recommend Linux/Mac/WSL

---

## 📖 目录结构简述 / Directory Structure

- `core/`：核心逻辑 / Core logic
- `api/`：客户端SDK与API定义 / Client SDK & API
- `example/`：示例代码 / Examples
- `MajulaNodeFromYaml.go`：节点启动入口 / Node entry
- `MajulaNode1.yaml`/`MajulaNode2.yaml`：节点配置样例 / Node config samples

---

## 💡 联系与贡献 / Contact & Contribute

如有建议、Bug反馈或想参与开发，欢迎提 Issue 或 PR！
For suggestions, bug reports, or contributions, feel free to open an Issue or PR!
