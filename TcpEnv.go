package main

import (
	"fmt"
	"strconv"
)

// ====================
// TcpEnv Definition
// ====================

type TcpEnv struct {
	Clients     map[string]*Node
	Servers     map[string]*Node
	ServersAddr map[string]string
	Token       string
	BasePort    int
	portOffset  int
}

func NewTcpEnv(basePort int, token string) *TcpEnv {
	return &TcpEnv{
		Clients:     make(map[string]*Node),
		Servers:     make(map[string]*Node),
		ServersAddr: make(map[string]string),
		BasePort:    basePort,
		Token:       token,
		portOffset:  0,
	}
}

func (env *TcpEnv) nextAddr() string {
	if env.BasePort == 0 {
		return "127.0.0.1:0"
	}
	port := env.BasePort + env.portOffset
	env.portOffset++
	return "127.0.0.1:" + strconv.Itoa(port)
}

func (env *TcpEnv) addSimpleServer(name string) {
	addr := env.nextAddr()

	node := NewNode(name)

	worker := NewTcpConnection(name, false, addr, "", nil, 4096, 10, 1000, 5, nil, env.Token)
	if worker == nil {
		panic("Failed to create TcpConnection for server: " + name + " at addr: " + addr)
	}

	actualAddr := addr
	if env.BasePort == 0 && worker.LocalAddr != nil {
		actualAddr = worker.LocalAddr.String()
		fmt.Println("The actual address is" + actualAddr)
	}

	channel := NewChannelFull(name+"-channel", node, worker)
	worker.User = channel
	node.addChannel(channel)

	env.Servers[name] = node
	env.ServersAddr[name] = actualAddr
}

func (env *TcpEnv) addSimpleClient(name string, serverName string) {
	serverAddr, ok := env.ServersAddr[serverName]
	if !ok {
		panic("Server not found: " + serverName)
	}

	localAddr := env.nextAddr()
	node := NewNode(name)

	worker := NewTcpConnection(name, true, localAddr, serverAddr, nil, 4096, 10, 1000, 5, nil, env.Token)
	if worker == nil {
		panic("Failed to create TcpConnection for client: " + name + " -> " + serverAddr)
	}

	channel := NewChannelFull(name+"-channel", node, worker)
	worker.User = channel
	channel.addChannelPeer(serverName)
	node.addChannel(channel)

	env.Clients[name] = node
}

func (env *TcpEnv) startAll() {
	for _, node := range env.Servers {
		go node.register()
	}
	for _, node := range env.Clients {
		go node.register()
	}
}

func (env *TcpEnv) printAllRoutingTables() {
	fmt.Println("=== Routing Tables ===")
	for _, node := range env.Servers {
		node.printRoutingTable()
	}
	for _, node := range env.Clients {
		node.printRoutingTable()
	}
}

func (env *TcpEnv) end() {
	for _, node := range env.Servers {
		node.quit()
	}
	for _, node := range env.Clients {
		node.quit()
	}

	// Close all TcpChannelWorkers
	for _, node := range env.Servers {
		for _, ch := range node.Channels {
			if ch.Worker != nil {
				ch.Worker.Close()
			}
		}
	}
	for _, node := range env.Clients {
		for _, ch := range node.Channels {
			if ch.Worker != nil {
				ch.Worker.Close()
			}
		}
	}
}

func (env *TcpEnv) connectClientToServer(clientName, serverName string) {
	clientNode, okClient := env.Clients[clientName]
	serverAddr, okServer := env.ServersAddr[serverName]
	if !okClient {
		panic("Client not found: " + clientName)
	}
	if !okServer {
		panic("Server not found: " + serverName)
	}

	localAddr := env.nextAddr()
	channelId := clientName + "-to-" + serverName

	worker := NewTcpConnection(channelId, true, localAddr, serverAddr, nil, 4096, 10, 1000, 5, nil, env.Token)
	if worker == nil {
		panic("Failed to connect " + clientName + " to " + serverName)
	}

	channel := NewChannelFull(channelId, clientNode, worker)
	worker.User = channel
	channel.addChannelPeer(serverName)

	clientNode.addChannel(channel)
}
