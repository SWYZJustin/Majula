# Firelink

**Firelink** is a lightweight, modular, and decentralized messaging system implemented in Go. It provides a distributed pub-sub architecture that supports automatic routing, peer-to-peer communication, and subscription propagation across the network.

Firelink is designed to be extensible and suitable for educational, experimental, or lightweight distributed applications. It enables nodes to discover each other, exchange topic-based messages, and maintain up-to-date routing and subscription tables.

It can be served as an idea reference and it keeps updating.

## Features

- **Decentralized architecture**: Each node maintains its own routing and link-state information.
- **Topic-based publish/subscribe**: Nodes can publish messages to topics and subscribe with client-defined callbacks.
- **Dynamic routing table construction**: Based on link cost and heartbeat propagation.
- **Flood-based subscription awareness**: Topic subscriptions are propagated periodically to ensure consistency.
- **TCP-based transport**: Nodes communicate over TCP using a pluggable channel abstraction.
- **Reliable message delivery**: Retry mechanisms are implemented for transient message delivery failures.
- **Extensible CLI**: A command-line interface allows interaction with the network during runtime.

## Installation

Ensure you have Go installed (version 1.18+ recommended).

```bash
git clone <your-firelink-repo>
cd firelink
go build -o firelink
