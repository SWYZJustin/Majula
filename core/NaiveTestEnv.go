package core

import (
	"fmt"
)

type Env struct {
	Nodes   map[string]*Node
	Clients map[string]*Client
}

func NewEnv() *Env {
	return &Env{
		Nodes:   make(map[string]*Node),
		Clients: make(map[string]*Client),
	}
}

func (e *Env) AddServer(nodeID string, bindAddr string, wsPort string) error {
	if _, exists := e.Nodes[nodeID]; exists {
		return fmt.Errorf("Node %s already exists", nodeID)
	}

	node := NewNode(nodeID)
	worker := NewTcpConnection(
		nodeID,
		false, // isServer
		bindAddr,
		"",
		nil,
		4096,
		10,
		1000,
		5,
		nil,
		"default_token",
	)
	if worker == nil {
		return fmt.Errorf("failed to create server TcpConnection for %s", nodeID)
	}

	channel := NewChannelFull(nodeID+"-channel", node, worker)
	worker.User = channel
	node.AddChannel(channel)
	node.Register()
	registerTestRpc(node)

	if wsPort != "" {
		go startUnifiedServer(node, wsPort)
	}

	e.Nodes[nodeID] = node
	fmt.Printf("[Env] Server Node '%s' started at %s\n", nodeID, bindAddr)
	return nil
}

// Add a client Node (connects to a server Node)
func (e *Env) AddClientNode(nodeID string, remoteAddr string, wsPort string) error {
	if _, exists := e.Nodes[nodeID]; exists {
		return fmt.Errorf("Node %s already exists", nodeID)
	}

	node := NewNode(nodeID)
	channelID := fmt.Sprintf("%s->%s", nodeID, remoteAddr)

	worker := NewTcpConnection(
		channelID,
		true, // isClient
		"",
		remoteAddr,
		nil,
		4096,
		10,
		1000,
		5,
		nil,
		"default_token",
	)
	if worker == nil {
		return fmt.Errorf("failed to connect client Node %s to %s", nodeID, remoteAddr)
	}

	channel := NewChannelFull(channelID, node, worker)
	worker.User = channel
	channel.addChannelPeer(remoteAddr)

	node.AddChannel(channel)
	node.Register()
	registerTestRpc(node)

	if wsPort != "" {
		go startUnifiedServer(node, wsPort)
	}

	e.Nodes[nodeID] = node
	fmt.Printf("[Env] Client Node '%s' connected to %s\n", nodeID, remoteAddr)
	return nil
}

func startUnifiedServer(node *Node, wsPort string) {
	server := NewServer(node, wsPort)
	router := SetupRoutes(server)
	err := router.Run(":" + wsPort)
	if err != nil {
		fmt.Printf("Server server failed to start on Port %s: %v\n", wsPort, err)
	}
}

func (e *Env) AddClient(clientID string) error {
	if _, exists := e.Clients[clientID]; exists {
		return fmt.Errorf("client %s already exists", clientID)
	}
	client := NewClient(clientID)
	e.Clients[clientID] = client
	fmt.Printf("[Env] Client '%s' created\n", clientID)
	return nil
}

func (e *Env) ConnectClientToNode(clientID, nodeID string) error {
	client, ok := e.Clients[clientID]
	if !ok {
		return fmt.Errorf("client %s not found", clientID)
	}
	node, ok := e.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("Node %s not found", nodeID)
	}
	return client.Connect(node)
}

func (e *Env) GetNode(nodeID string) *Node {
	return e.Nodes[nodeID]
}

func (e *Env) GetClient(clientID string) *Client {
	return e.Clients[clientID]
}

func (e *Env) PrintRouting(nodeID string) {
	node, ok := e.Nodes[nodeID]
	if !ok {
		fmt.Printf("[Env] Node %s not found\n", nodeID)
		return
	}
	node.printRoutingTable()
}

func (e *Env) Shutdown() {
	for id, node := range e.Nodes {
		fmt.Printf("[Env] Shutting down Node %s\n", id)
		node.Quit()
	}
	for id, client := range e.Clients {
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
			return fmt.Sprintf("Hello from Node '%s'!", to)
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
