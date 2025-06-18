package main

import "fmt"

type Env struct {
	nodes   map[string]*Node
	clients map[string]*Client
}

func NewEnv() *Env {
	return &Env{
		nodes:   make(map[string]*Node),
		clients: make(map[string]*Client),
	}
}

func (e *Env) AddServer(nodeID string, bindAddr string) error {
	if _, exists := e.nodes[nodeID]; exists {
		return fmt.Errorf("node %s already exists", nodeID)
	}

	node := NewNode(nodeID)
	worker := NewTcpConnection(
		nodeID,
		false, // isServer
		bindAddr,
		"",
		nil,
		defaultMaxFrameSize,
		defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize,
		defaultMaxConnectionsPerSec,
		nil,
		defaultToken,
	)
	if worker == nil {
		return fmt.Errorf("failed to create server TcpConnection for %s", nodeID)
	}

	channel := NewChannelFull(nodeID+"-channel", node, worker)
	worker.User = channel
	node.addChannel(channel)
	node.register()
	registerTestRpc(node)

	e.nodes[nodeID] = node
	fmt.Printf("[Env] Server node '%s' started at %s\n", nodeID, bindAddr)
	return nil
}

// Add a client node (connects to a server node)
func (e *Env) AddClientNode(nodeID string, remoteAddr string) error {
	if _, exists := e.nodes[nodeID]; exists {
		return fmt.Errorf("node %s already exists", nodeID)
	}

	node := NewNode(nodeID)
	channelID := fmt.Sprintf("%s->%s", nodeID, remoteAddr)

	worker := NewTcpConnection(
		channelID,
		true, // isClient
		"",
		remoteAddr,
		nil,
		defaultMaxFrameSize,
		defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize,
		defaultMaxConnectionsPerSec,
		nil,
		defaultToken,
	)
	if worker == nil {
		return fmt.Errorf("failed to connect client node %s to %s", nodeID, remoteAddr)
	}

	channel := NewChannelFull(channelID, node, worker)
	worker.User = channel
	channel.addChannelPeer(remoteAddr)

	node.addChannel(channel)
	node.register()
	registerTestRpc(node)

	e.nodes[nodeID] = node
	fmt.Printf("[Env] Client node '%s' connected to %s\n", nodeID, remoteAddr)
	return nil
}

func (e *Env) AddClient(clientID string) error {
	if _, exists := e.clients[clientID]; exists {
		return fmt.Errorf("client %s already exists", clientID)
	}
	client := NewClient(clientID)
	e.clients[clientID] = client
	fmt.Printf("[Env] Client '%s' created\n", clientID)
	return nil
}

func (e *Env) ConnectClientToNode(clientID, nodeID string) error {
	client, ok := e.clients[clientID]
	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}
	node, ok := e.nodes[nodeID]
	if !ok {
		return fmt.Errorf("node %s not found", nodeID)
	}
	return client.Connect(node)
}

func (e *Env) GetNode(nodeID string) *Node {
	return e.nodes[nodeID]
}

func (e *Env) GetClient(clientID string) *Client {
	return e.clients[clientID]
}

func (e *Env) PrintRouting(nodeID string) {
	node, ok := e.nodes[nodeID]
	if !ok {
		fmt.Printf("[Env] Node %s not found\n", nodeID)
		return
	}
	node.printRoutingTable()
}

func (e *Env) Shutdown() {
	for id, node := range e.nodes {
		fmt.Printf("[Env] Shutting down node %s\n", id)
		node.quit()
	}
	for id, client := range e.clients {
		fmt.Printf("[Env] Disconnecting client %s\n", id)
		client.Disconnect()
	}
}

func registerTestRpc(node *Node) {
	node.registerRpcService(
		"whoami",
		"default",
		RPC_FuncInfo{},
		func(fun string, params map[string]interface{}, from string, to string, invokeId int64) interface{} {
			return fmt.Sprintf("Hello from node '%s'!", to)
		},
	)

	node.registerRpcService(
		"add",
		"default",
		RPC_FuncInfo{},
		func(fun string, params map[string]interface{}, from string, to string, invokeId int64) interface{} {
			aVal, aOk := params["a"].(float64)
			bVal, bOk := params["b"].(float64)
			if !aOk || !bOk {
				return map[string]interface{}{
					"error": "parameters 'a' and 'b' must be numbers",
				}
			}
			return map[string]interface{}{
				"sum": aVal + bVal,
			}
		},
	)
}
