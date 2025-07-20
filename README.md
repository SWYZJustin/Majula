# Majula Distributed Communication Middleware

Majula is a high-performance, distributed communication middleware written in Go. It provides robust node-to-node messaging, RPC, topic-based pub/sub, NAT traversal (FRP), dynamic Nginx reverse proxy, and more. Majula is ideal for microservices, distributed systems, NAT traversal, and real-time messaging scenarios.

---

## 🌟 Features

- **Distributed Node Management**: Automatic node discovery, heartbeat, and link management.
- **High-Performance Message Routing**: Point-to-point, topic pub/sub, and broadcast messaging.
- **RPC (Remote Procedure Call)**: Register and invoke custom RPC services between nodes, supporting sync/async calls.
- **WebSocket & HTTP APIs**: Unified, extensible API for both WebSocket and RESTful HTTP clients.
- **FRP NAT Traversal**: Built-in FRP tunneling for seamless node-to-node communication and file transfer across NATs.
- **Dynamic Nginx Reverse Proxy**: Register and expose local services to remote nodes via HTTP mapping.
- **Extensible Architecture**: Modular, easy to extend and integrate into your own systems.

---

## 🚀 Quick Start

### 1. Install Dependencies
```bash
go mod tidy
```

### 2. Start Local Nodes (Example)
```bash
nohup go run MajulaNodeFromYaml.go MajulaNode1.yaml &
nohup go run MajulaNodeFromYaml.go MajulaNode2.yaml &
```
> You can customize node configuration via `MajulaNodeTemplate.yaml`.

### 3. Connect and Test
- Use a WebSocket client or curl/Postman for HTTP testing.
- Or use `api/MajulaClient.go` as a Go SDK for your own applications.

---

## 📡 HTTP API Overview

All endpoints are under `/majula`, supporting both GET and POST.

| Path         | Description             | Main Params         |
|--------------|------------------------|---------------------|
| /ws          | WebSocket connection   | target (optional)   |
| /h           | HTTP message send/recv | see below           |
| /sub         | Subscribe topic        | topic               |
| /pub         | Publish topic message  | topic, args         |
| /rpc         | RPC call               | fun, args, ...      |
| /send        | Private message        | target_node, ...    |
| /list_rpc    | List RPC services      |                     |
| /map         | Nginx proxy mgmt       | see below           |
| /frp         | FRP mgmt               | see below           |
| /upload      | File upload            |                     |
| /download    | File download          |                     |

**Examples:**
```bash
curl -X POST http://localhost:8080/majula/sub -d '{"topic":"test"}'
curl -X POST http://localhost:8080/majula/pub -d '{"topic":"test","args":{"msg":"hello"}}'
curl -X POST http://localhost:8080/majula/rpc -d '{"fun":"add","args":{"a":1,"b":2}}'
```

---

## 🔗 WebSocket API (Recommended)

### Connection
```
ws://localhost:8080/majula/ws/{client_id}
```

### Message Format
All WebSocket messages are JSON objects with the following structure:
```json
{
  "method": "SUBSCRIBE|PUBLISH|RPC|SEND|REGISTER_RPC|UNREGISTER_RPC|QUIT|...",
  "topic": "test",           // Topic (optional)
  "fun": "add",              // RPC function name (optional)
  "args": {"a":1,"b":2},    // Parameters (optional)
  "invokeid": 123,           // Invoke ID (optional)
  "result": null             // Result (for server responses)
}
```

#### Common Methods
- `SUBSCRIBE`: Subscribe to a topic
- `UNSUBSCRIBE`: Unsubscribe from a topic
- `PUBLISH`: Publish a message to a topic
- `RPC`: Call a remote RPC function
- `REGISTER_RPC`: Register a local RPC service
- `UNREGISTER_RPC`: Unregister a local RPC service
- `SEND`: Send a private (P2P) message
- `QUIT`: Disconnect

#### Server Push Example
```json
{
  "method": "SUB_RESULT",
  "topic": "test",
  "args": {"msg":"hello"}
}
```

---

## 🧩 Go SDK: WebSocket Client API (`api/apis.go`)

Majula provides a high-level Go SDK for WebSocket communication and API calls. See `api/apis.go` for full details.

### Main Methods
- `NewClient(addr, entity)`: Create and connect a WebSocket client
- `CallRpc(fun, args, targetNode, provider, timeout)`: Synchronous remote RPC call
- `RegisterRpc(fun, handler, meta)`: Register a local RPC function
- `CallRpcAsync(...)`: Asynchronous RPC call
- `Subscribe(topic, handler)`: Subscribe to a topic
- `Unsubscribe(topic)`: Unsubscribe from a topic
- `Publish(topic, args)`: Publish a message to a topic
- `OnPrivate(handler)`: Set private message handler
- `SendPrivate(targetNode, targetClient, payload)`: Send a private message
- `RegisterFRP(...)`, `RegisterNginxFRP(...)`: FRP/Nginx operations
- `UploadFile(...)`, `DownloadFile(...)`: File transfer
- `Quit()`: Close the client connection

#### Example
```go
client := api.NewClient("ws://localhost:8080", "my-client")
client.Subscribe("test", func(topic string, args map[string]interface{}) {
    fmt.Println("Received:", topic, args)
})
client.Publish("test", map[string]interface{}{"msg": "hello"})
res, ok := client.CallRpc("add", map[string]interface{}{"a":1, "b":2}, "targetNode", "default", time.Second)
if ok {
    fmt.Println("RPC result:", res)
}
client.Quit()
```

---

## 🛠️ Advanced Features

- **FRP NAT Traversal**: Register/start FRP tunnels via `/majula/frp` or SDK for seamless node-to-node communication.
- **Nginx Reverse Proxy**: Dynamically register local HTTP services to remote nodes via `/majula/map` or SDK.
- **File Transfer**: Upload/download files between nodes.

---

## ⚙️ Dependencies & Build
- Go 1.18+
- See `go.mod` for dependencies
- Recommended: Linux/Mac/WSL environment

---

## 📖 Directory Structure
- `core/`: Core logic (nodes, channels, messages, RPC, FRP, Nginx, etc.)
- `api/`: Client SDK & API definitions
- `example/`: Example code
- `MajulaNodeFromYaml.go`: Node startup entry
- `MajulaNode1.yaml`/`MajulaNode2.yaml`: Node config samples

---

## 💡 Contact & Contribute
For suggestions, bug reports, or contributions, feel free to open an Issue or PR!

---

## 🚧 Planned Features

- **Client Election**: Implement distributed client election to support leader selection and failover scenarios.
- **Consistency Features**: Add distributed consistency mechanisms (such as consensus protocols, state synchronization, etc.) to ensure data reliability and coordination across nodes.
- **Network & Other Optimizations**: Further optimize network performance, resource usage, and add more advanced features for scalability and robustness.
