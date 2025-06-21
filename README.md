# Usage Guide for Majula [Version 0.1]
![Logo](./Majula_Cover.png)

Majula is a lightweight peer-to-peer messaging framework supporting topic-subscription, RPC, and optional HTTP/WebSocket access. In the future, it aims to support FRP and nginx in some way. This guide introduces how to quickly get started with the system after building and running the application.

Currently it's still under development. A lot of ideas have not been achieved to make it a usable system, especially, the apis are not provided perfectly and the functionalities are not fully achieved. So currently, it could only be seen as a toy program.

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
* `ws=<port>`: (suggested) Starts a WebSocket server on the specified port, it will enable websocket or http connection to the system.
The most suggested way to create a node is to use the websocket connection. It will allow you to access a node with a remote client.



### Quickstart [Not suggested, only for test uses]

Quickstart, following with the local client creation, are only used for test at the beginning of the development. These commands could be skipped.
But if you are interested in doing a test, feel free to use them.

Creates a node, a client connected to it, and logs in:

```bash
quickstart server <nodeID> <addr>
```

---

## Managing Clients

### Add a Client [Not suggested, only for test uses, same for the next several commands]

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

## WebSocket Client Mode [suggested]

As described above, currently the more suggested way is to use a web client. You could either creating a websocket client or using post or get access.

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
* `MajulaServer.go & MajulaClient.go` – WebSocket/HTTP client integration
* `SimpleLink.go` – user command prompt

---

## Goals & Roadmap

Current features:

* Peer-to-peer node communication
* Topic-based pub-sub
* RPC calls across nodes
* WebSocket + HTTP support

Planned features:

* Message sequencing guarantees (under progress) (TCP style)
* Functional Reactive Programming (FRP)
* Partial NGINX behavior emulation
* apis exported to increase functionalities of the system
* More improvements on the safety and efficiency
---

For any contribution or question, feel free to open an issue or contact the repository maintainer (me)
Enjoy
