# Majula - Distributed Communication Middleware

> ⚠️ **Important Notice**: This project is currently in testing and optimization phase. Many potential issues exist. Not recommended for production use. Testing and feedback are welcome, but please use with caution.

Majula is a lightweight distributed communication middleware written in Go. It provides inter-node message passing, RPC, topic-based publish/subscribe, NAT traversal, dynamic Nginx reverse proxy, and more. Majula is suitable for microservices, distributed systems, NAT traversal, and real-time messaging scenarios.

The name "Majula" comes from Dark Souls 2, representing the game's Firelink Shrine - perhaps the warmest place in the Souls series. People meet during their adventures and gather around the Firelink Shrine. I hope my middleware can help connect people - or more likely, devices.

---

## 🌟 Core Features

### Distributed Node Management
Each node has a unique ID and supports node discovery, heartbeat detection, and link management. Nodes establish connections via TCP or KCP protocols, forming network topologies. The system maintains inter-node connection states and handles node online/offline scenarios.

### Lightweight Message Routing
Supports point-to-point direct communication, topic-based publish/subscribe, and broadcast messaging. Message routing selects paths based on target nodes and supports message retry mechanisms. The publish/subscribe pattern allows nodes to subscribe to topics and receive relevant messages.

### RPC Remote Invocation
Supports registering and invoking custom services between nodes. Supports both synchronous and asynchronous invocation modes, with the ability to specify target nodes and service providers. The RPC system handles network transmission, serialization, and other details.

### WebSocket & HTTP API
Provides unified API interfaces supporting WebSocket and HTTP clients. WebSocket interfaces offer real-time bidirectional communication, while HTTP interfaces facilitate integration and testing. All APIs use JSON data format.

### High-Performance KCP Channels
In addition to TCP connections, supports UDP-based KCP channels. KCP channels provide lower latency in weak network environments, suitable for scenarios requiring high network quality.

---

## 🚀 Quick Start

### 1. Install Dependencies
```bash
go mod tidy
```

### 2. Start Signaling Server (Optional)
```bash
go run SignalingServerFromYaml.go
```

### 3. Start Local Node
```bash
# Use default configuration (no signaling server connection)
go run MajulaNodeFromYaml.go

# Use custom configuration
go run MajulaNodeFromYaml.go MajulaNode1.yaml

# Enable signaling server connection
go run MajulaNodeFromYaml.go MajulaNodeWithSignaling.yaml
```

### Channel Protocol Configuration
Majula supports both TCP and KCP channel protocols. TCP channels provide reliable ordered transmission with TLS encryption support. KCP channels are UDP-based, performing better in weak network environments but without TLS support.

---

## 📡 API Interface Overview

Majula provides API interfaces with all endpoints under the `/majula` path, supporting GET and POST methods.

### Core Function Interfaces
- **WebSocket Connection**: Provides real-time bidirectional communication
- **Message Sending/Receiving**: Supports point-to-point and broadcast message delivery
- **Topic Subscription**: Supports topic-based publish/subscribe patterns
- **RPC Invocation**: Supports remote procedure calls, including synchronous and asynchronous modes
- **Private Messages**: Supports private message delivery between nodes

### Advanced Function Interfaces
- **RPC Service Management**: Register, unregister, and query RPC services
- **Nginx Proxy Management**: Dynamically configure reverse proxy rules
- **FRP Tunnel Management**: Configure and manage NAT traversal tunnels
- **File Transfer**: Supports file upload and download between nodes

### System Management Interfaces
- **Node Information**: Query node status and connection information
- **Health Check**: Monitor system operational status
- **Configuration Management**: Dynamically adjust system configuration

---

## 🧩 Development Tools

### Go SDK
Majula provides a Go SDK that encapsulates core functionality. The SDK offers high-level APIs that handle underlying network communication details.

### Client Libraries
Provides client libraries supporting both WebSocket and HTTP communication methods. Client libraries handle connection management, message serialization, error retry, and other details.

### Configuration Management
Supports YAML format configuration files for node parameters, network settings, security options, and more.

---

## 🌐 FRP NAT Traversal

### Tunnel Functionality
Built-in FRP tunnel functionality solves inter-node communication problems in NAT environments. Supports port mapping, file transfer, service exposure, and more. Through FRP tunnels, nodes in different network environments can establish direct connections.

### Dynamic Port Mapping
Supports dynamic configuration of port mapping rules without manual firewall configuration. Nodes can automatically register and deregister port mappings, achieving flexible network access control.

### File Transfer
Implements file transfer functionality between nodes based on FRP tunnels. Supports large file transfer, resume from breakpoint, transfer progress monitoring, and other features.

### Service Exposure
Can expose local services to remote nodes through FRP tunnels, enabling cross-network service access. Supports HTTP, TCP, UDP, and other protocols.

---

## 🔄 Dynamic Nginx Reverse Proxy

### Proxy Functionality
Provides dynamic Nginx reverse proxy functionality to expose local services to remote nodes. Supports HTTP service mapping, load balancing, and more. Proxy rules can be dynamically configured through API calls.

### Dynamic Configuration
Supports runtime dynamic addition, modification, and deletion of proxy rules without service restart. Real-time proxy configuration updates can be achieved through simple API calls.

### Load Balancing
Supports multiple load balancing strategies, automatically adjusting traffic distribution based on node load conditions. Provides health check functionality to automatically remove faulty nodes.

### SSL Termination
Supports SSL certificate management and HTTPS proxy, providing secure encrypted communication. Multiple domains and certificates can be configured for flexible SSL management.

---

## 📡 Signaling Server

### UDP Hole Punching
Provides a signaling server based on UDP hole punching technology, supporting automatic node discovery and connection coordination. The signaling server acts as an intermediary between nodes, helping them discover each other and establish direct connections. NAT traversal is achieved through UDP hole punching technology, suitable for various NAT environments.

### Connection Coordination
When nodes need to establish P2P connections, the signaling server assists in exchanging connection information for UDP hole punching. Supports penetration strategies for multiple NAT types, including symmetric, cone, and port-restricted NATs.

### State Management
Maintains state information for all connected nodes, including node online status, connection quality, service capabilities, and more. Provides node status query and monitoring functionality.

### Message Relay
When direct connections cannot be established, the signaling server can act as a message relay to ensure communication reliability between nodes.

---

## ⚡ Distributed Consensus

### Raft Consensus Algorithm
Majula implements the Raft consensus algorithm, providing strongly consistent distributed data management. Each Raft group has independent leader election, log replication, and state machines.

### Multi-Group Support
A single node can participate in multiple independent Raft groups, each managing different data. This design allows data partitioning based on business requirements.

### Static Core Cluster
Core nodes are statically defined in configuration files, ensuring cluster stability. Core nodes participate in all consensus decisions, guaranteeing data consistency.

### Dynamic Learners
Supports dynamically adding learner nodes that can read data but do not participate in consensus decisions. Suitable for data synchronization, backup, and other scenarios.

---

## 🏛️ Distributed Election

### Lightweight Design
The election system adopts a lightweight design, not relying on complex consensus algorithms, providing fast failover capabilities. Suitable for scenarios requiring high availability but not strict consistency.

### Three-State Mechanism
Nodes have three states during election: busy (initializing), standby (ready to take over), and on-duty (current leader). State transitions are based on heartbeat and timeout mechanisms.

### Failure Detection
Detects node failures through heartbeat mechanisms. When a leader node fails, standby nodes take over. Failure detection time is configurable.

### Multiple Election Groups
Supports multiple independent election groups, each conducting leader election independently. Suitable for high availability requirements of different business modules.

### Application Scenarios
- **API Gateway High Availability**: Multiple gateway nodes, one active
- **Task Scheduler**: Avoid duplicate task execution
- **Service Discovery**: Primary service coordinator
- **Load Balancer**: Primary load balancer with backup

---

## 🔧 Implemented Features

### Network Optimization
- **Connection Pool Management**: Manages connection pools for connection reuse
- **Traffic Control**: Implements connection rate limiting to prevent system overload
- **Timeout Retry**: Handles network timeout and retry logic

### Security Mechanisms
- **TLS Encryption**: Supports TLS encrypted communication
- **Identity Authentication**: Supports token-based identity authentication
- **Access Control**: Supports IP whitelist and access permission control

### Monitoring and Debugging
- **Logging System**: Supports structured logging and log level control
- **Health Check**: Provides system health status check interfaces
- **Debug Tools**: Provides debugging interfaces and tools

---

## ⚙️ System Requirements

- **Go Version**: 1.18 or higher
- **Operating System**: Supports Linux, macOS, Windows
- **Network**: Supports TCP/UDP network communication
- **Memory**: Recommend at least 512MB available memory
- **Storage**: Supports local file system storage

---

## 📖 Project Structure

- **core/**: Core logic modules, including node management, message routing, RPC framework, etc.
- **api/**: Client SDK and API definitions
- **server/**: Signaling server implementation
- **example/**: Example code and usage demonstrations
- **MajulaNodeFromYaml.go**: Node startup entry program
- **SignalingServerFromYaml.go**: Signaling server startup entry program
- **MajulaNodeTemplate.yaml**: Node configuration template
- **SignalingServerTemplate.yaml**: Signaling server configuration template

---

## 💡 Contact and Contributions

For suggestions, bug reports, or contributions, please submit Issues or PRs! We welcome any form of contribution, including but not limited to:

- Feature suggestions and requirement feedback
- Code improvements and optimizations
- Documentation improvements and translations
- Test cases and example code
- Performance optimizations and bug fixes
