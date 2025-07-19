package core

import (
	"fmt"
)

type ClientApp struct {
	Node   *Node
	Worker *TcpChannelWorker
	Token  string
}

func NewClientApp(nodeID string, isServer bool, bindAddr string, token string) *ClientApp {
	node := NewNode(nodeID)

	var worker *TcpChannelWorker
	if isServer {
		worker = NewTcpConnection(
			nodeID, false, bindAddr, "", nil,
			4096, 10, 1000, 5, nil, token,
		)
	} else {
		worker = nil
	}

	app := &ClientApp{
		Node:   node,
		Worker: worker,
		Token:  token,
	}

	if worker != nil {
		channel := NewChannelFull(nodeID+"-channel", node, worker)
		worker.User = channel
		node.addChannel(channel)
		node.register()
		fmt.Println("Started server at", bindAddr)
	}

	return app
}

func (app *ClientApp) ConnectToPeer(peerAddr string) error {
	localAddr := "127.0.0.1:0"
	channelID := fmt.Sprintf("%s->%s", app.Node.ID, peerAddr)

	worker := NewTcpConnection(
		channelID, true, localAddr, peerAddr, nil,
		4096, 10, 1000, 5, nil, app.Token,
	)

	if worker == nil {
		return fmt.Errorf("failed to connect to %s", peerAddr)
	}

	channel := NewChannelFull(channelID, app.Node, worker)
	worker.User = channel
	channel.addChannelPeer(peerAddr)
	app.Node.addChannel(channel)
	app.Node.register()

	fmt.Printf("Connected to peer at %s\n", peerAddr)
	return nil
}

func (app *ClientApp) Send(toID, content string) {
	msg := &Message{
		MessageData: MessageData{
			Type: Other,
			Data: content,
		},
		From:       app.Node.ID,
		To:         toID,
		TTL:        10,
		LastSender: app.Node.ID,
	}
	app.Node.sendTo(toID, msg)
	fmt.Printf("Sent message to %s: %s\n", toID, content)
}

func (app *ClientApp) PrintRouting() {
	app.Node.printRoutingTable()
}

func (app *ClientApp) Shutdown() {
	app.Node.quit()
	if app.Worker != nil {
		app.Worker.Close()
	}
	fmt.Println("Node shut down.")
}

func NewSimpleServer(nodeID string, listenAddr string) *ClientApp {
	node := NewNode(nodeID)

	worker := NewTcpConnection(
		nodeID,
		false,
		listenAddr,
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
		panic("Failed to create server TcpConnection")
	}

	channel := NewChannelFull(nodeID+"-channel", node, worker)
	worker.User = channel
	node.addChannel(channel)
	node.register()

	fmt.Printf("Server Node '%s' started on %s\n", nodeID, listenAddr)
	return &ClientApp{Node: node, Worker: worker, Token: defaultToken}
}

func NewSimpleClient(nodeID string, remoteAddr string) *ClientApp {
	node := NewNode(nodeID)

	localAddr := "127.0.0.1:0"
	channelID := fmt.Sprintf("%s->%s", nodeID, remoteAddr)

	worker := NewTcpConnection(
		channelID,
		true, // isClient
		localAddr,
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
		panic("Failed to connect to remote server")
	}

	channel := NewChannelFull(channelID, node, worker)
	worker.User = channel
	channel.addChannelPeer(remoteAddr)
	node.addChannel(channel)
	node.register()

	fmt.Printf("Client Node '%s' connected to %s\n", nodeID, remoteAddr)
	return &ClientApp{Node: node, Worker: worker, Token: defaultToken}
}

func (app *ClientApp) Subscribe(topic, clientName string, cb MESSAGE_CALLBACK) {
	app.Node.addLocalSub(topic, clientName, cb)
}

func (app *ClientApp) Unsubscribe(topic, clientName string) {
	app.Node.removeLocalSub(topic, clientName)
}

func (app *ClientApp) Publish(topic, message string) {
	app.Node.publishOnTopic(topic, message)
}

func (app *ClientApp) PrintRoutingTable() {
	app.Node.printRoutingTable()
}

func (app *ClientApp) PrintTotalSubs() {
	app.Node.PrintTotalSubs()
}
