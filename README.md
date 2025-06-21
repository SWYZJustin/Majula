# Usage Guide for Majula
![Logo](./Majula_Cover.png)

Majula is a lightweight peer-to-peer messaging framework supporting topic-subscription, RPC, and optional HTTP/WebSocket access. This guide introduces how to quickly get started with the system after building and running the application.

---

## Getting Started

### Build & Run

Ensure Go is installed, then build and run the Majula binary:

```bash
go build
./Majula.exe
```

This will start the command-line controller for Majula.

---

## Creating Nodes and Clients

### Starting a Node

```bash
start server <nodeID> <listenAddr>
start client <nodeID> <remoteAddr>
start server|client <nodeID> <addr> [ws=<port>]
```

* `nodeID`: Unique identifier for the node.
* `listenAddr`/`remoteAddr`: TCP address such as `127.0.0.1:8001`.
* `ws=<port>`: (optional) Starts a WebSocket server on the specified port.

### Quickstart

Creates a node, a client connected to it, and logs in:

```bash
quickstart server <nodeID> <addr>
```

---

## Managing Clients

### Add a Client

```bash
addclient <clientName>
```

### Connect Client to Node

```bash
connectclient <clientName> <nodeID>
```

### Login to Client Console

```bash
login <clientName>
```

This switches the prompt into client mode.

---

## Client Mode Commands

```bash
sub <topic>                 # Subscribe to a topic
unsub <topic>               # Unsubscribe from a topic
pub <topic> <message>       # Publish message to a topic
sayhello <targetNodeID>     # RPC 'whoami' call
add <targetNodeID> a+b      # RPC 'add' function
exit                        # Exit client session
```

---

## WebSocket Client Mode

You can run a WebSocket client session with:

```bash
wlogin <clientId> <ws-url>
```

Once connected, you can use the following commands:

```bash
sub <topic>                                 # Subscribe to a topic
unsub <topic>                               # Unsubscribe from a topic
pub <topic> <json>                          # Publish JSON data to a topic
rpc <fun> <targetNode> <provider> <json>   # Make an RPC call
listrpc <targetNode> <provider>            # List available RPCs from a provider
send <targetNode> <targetClient> <json>    # Send private message to client on target node
exit                                        # Exit session
```

Example:

```bash
send s2 alice {"msg": "Hello Alice!"}
```

---

## HTTP/WebSocket API

Majula exposes both HTTP and WebSocket routes via Gin. Below are the available endpoints:

### WebSocket

* `GET /ws/:target` — Connect as WebSocket client with ID `target`

### HTTP (SSE + JSON)

* `GET /http/:target` — Receive events as Server-Sent Events
* `POST /http/:target` — Send JSON messages

### HTTP GET Helpers

* `/send?to_node=X&to_client=Y&msg=Z`
* `/pub?topic=foo&msg=hello`
* `/sub?topic=foo`
* `/rpc?fun=add&to_node=s2&provider=default&args={}`
* `/listrpc?to_node=s2&provider=default`

These endpoints allow remote interaction with the messaging layer using standard HTTP clients or curl.

---

## File Reference

Most core functionalities are defined in:

* `Node.go` – Node and routing logic
* `Channel.go` – Message relaying logic
* `TCPChannelWorker.go` – TCP communication
* `RPC.go` – Remote procedure call framework
* `Server.go` – WebSocket/HTTP client integration

---

## Goals & Roadmap

Current features:

* Peer-to-peer node communication
* Topic-based pub-sub
* RPC calls across nodes
* WebSocket + HTTP support

Planned features:

* Functional Reactive Programming (FRP)
* Partial NGINX behavior emulation
* Message sequencing guarantees (TCP style)

---

For any contribution or question, feel free to open an issue or contact the repository maintainer.
