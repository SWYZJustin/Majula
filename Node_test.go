package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"net"
	"sync"
	"testing"
	"time"
)

func TestBuildRoutingTable(t *testing.T) {
	node := &Node{
		ID: "A",
		LinkSet: LinkSetType{
			"A": {
				"B": {Source: "A", Target: "B", Cost: 1, Channel: "ch1"},
				"C": {Source: "A", Target: "C", Cost: 2, Channel: "ch2"},
			},
			"B": {
				"C": {Source: "B", Target: "C", Cost: 2, Channel: "ch3"},
			},
			"C": {},
			"D": {},
		},
		RoutingTable: make(RoutingTableType),
		LinkSetMutex: sync.RWMutex{},
	}
	node.buildRoutingTable()
	fmt.Println("Routing Table for Node A:")
	for target, routes := range node.RoutingTable {
		fmt.Printf("Target: %s, Next Hop: %s, Channel: %s\n", target, routes[0].nextHopNodeID, routes[0].LocalChannelID)
	}
}

func TestRunEnv(t *testing.T) {
	nodes := []string{"A", "B", "C"}

	pairs := []SimplePair{
		newSimplePair([]string{"A", "B"}),
		newSimplePair([]string{"B", "C"}),
	}

	env := NewSimpleEnvEx(nodes, pairs)
	env.show()

	fmt.Println("Starting the environment...")
	env.runOld()
	fmt.Println("Environment stopped.")
}

func TestRunEnv_Complex(t *testing.T) {
	nodes := []string{"A", "B", "C", "D", "E", "F"}

	// More connections, including cycles and redundant paths
	pairs := []SimplePair{
		newSimplePair([]string{"A", "B"}),
		newSimplePair([]string{"A", "C"}),
		newSimplePair([]string{"B", "C"}),
		newSimplePair([]string{"C", "D"}),
		newSimplePair([]string{"D", "E"}),
		newSimplePair([]string{"E", "F"}),
		newSimplePair([]string{"F", "A"}), // cycle back to A
		newSimplePair([]string{"B", "E"}), // shortcut path
	}

	env := NewSimpleEnvEx(nodes, pairs)
	env.show()

	fmt.Println("Starting the complex environment...")
	env.runOld()
	fmt.Println("Environment stopped.")
}

type DummyNode struct {
	LinkSet      LinkSetType
	LinkSetMutex sync.RWMutex
}

func (node *DummyNode) serializeLinkSet() string {
	node.LinkSetMutex.RLock()
	defer node.LinkSetMutex.RUnlock()
	filteredLinkSet := make(LinkSetType)

	for key1, innerMap := range node.LinkSet {
		filteredLinkSet[key1] = make(map[string]Link)
		for key2, link := range innerMap {
			if link.Cost != -1 {
				filteredLinkSet[key1][key2] = link
			}
		}
	}
	data, err := json.Marshal(filteredLinkSet)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestLinkSetSerialization(t *testing.T) {
	linkSet := LinkSetType{
		"A": {
			"B": Link{
				Source:  "A",
				Target:  "B",
				Cost:    123,
				Version: 1,
				Channel: "ch1",
			},
		},
	}

	node := DummyNode{
		LinkSet: linkSet,
	}

	serialized := node.serializeLinkSet()
	t.Logf("Serialized: %s", serialized)

	deserialized := deserializeLinkSet(serialized)
	if deserialized == nil {
		t.Errorf("Deserialization returned nil")
	} else {
		t.Logf("Deserialized: %+v", deserialized)
	}
}

/*
    C1       C2
     |        |
    S1       S2
     \      /
       C3 (bridge)
     /      \
   S3        S4
    |        |
   C4       C5

*/

func TestRunBridgeTopologyEnv(t *testing.T) {
	env := NewTcpEnv(25555, "bridge-token")
	env.addSimpleServer("S1")
	env.addSimpleServer("S2")
	env.addSimpleServer("S3")
	env.addSimpleServer("S4")
	env.addSimpleClient("C1", "S1")
	env.addSimpleClient("C2", "S2")
	env.addSimpleClient("C4", "S3")
	env.addSimpleClient("C5", "S4")
	env.addSimpleClient("C3", "S1")
	env.connectClientToServer("C3", "S2")
	env.connectClientToServer("C3", "S3")
	env.connectClientToServer("C3", "S4")
	env.startAll()

	time.Sleep(10 * time.Second)
	env.end()
	time.Sleep(2 * time.Second)
	env.printAllRoutingTables()
}

func TestRunTcpEnv(t *testing.T) {
	env := NewTcpEnv(22223, "test-token")

	// Setup TCP nodes
	env.addSimpleServer("S1")
	env.addSimpleClient("C1", "S1")
	env.addSimpleClient("C2", "S1")

	env.startAll()
	time.Sleep(10 * time.Second)
	env.end()
	time.Sleep(2 * time.Second)
	env.printAllRoutingTables()

}

func TestClientServerCommunication(t *testing.T) {
	node := NewNode("test-node")
	server := NewServer(node)

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", server.handleWS)
		err := r.Run(":18080")
		if err != nil {
			t.Errorf("Gin server failed: %v", err)
		}
	}()
	time.Sleep(time.Second)

	url := "ws://localhost:18080/ws/test-client"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	registerMsg := MajulaPackage{
		Method: "REGISTER_CLIENT",
	}
	msgBytes, _ := json.Marshal(registerMsg)
	err = conn.WriteMessage(websocket.TextMessage, msgBytes)
	if err != nil {
		t.Fatalf("Failed to send register message: %v", err)
	}

	time.AfterFunc(500*time.Millisecond, func() {
		server.SendToClient("test-client", MajulaPackage{
			Method: "PUBLISH",
			Topic:  "test-topic",
			Args: map[string]interface{}{
				"msg": "hello-from-server",
			},
		})
	})

	_, recv, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message from server: %v", err)
	}
	t.Logf("Client received: %s", string(recv))
}

func TestRpcCommunication(t *testing.T) {

	serverNode := NewNode("serverNode")
	serverWorker := NewTcpConnection(
		"serverNode", false, "127.0.0.1:29090", "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec,
		nil, defaultToken,
	)
	if serverWorker == nil {
		t.Fatal("Failed to create server TcpConnection")
	}
	serverChannel := NewChannelFull("serverChannel", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.addChannel(serverChannel)
	serverNode.register()

	serverNode.registerRpcService("whoami", "default", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		return fmt.Sprintf("Hello from %s", to)
	})

	clientNode := NewNode("clientNode")
	clientWorker := NewTcpConnection(
		"clientNode", true, "", "127.0.0.1:29090", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec,
		nil, defaultToken,
	)
	if clientWorker == nil {
		t.Fatal("Failed to create client TcpConnection")
	}
	clientChannel := NewChannelFull("clientChannel", clientNode, clientWorker)
	clientWorker.User = clientChannel
	clientChannel.addChannelPeer("serverNode") // 明确 server 的 ID
	clientNode.addChannel(clientChannel)
	clientNode.register()

	// 3. 等待两边连接初始化、路由表生成
	time.Sleep(2 * time.Second)

	result, ok := clientNode.makeRpcRequest("serverNode", "default", "whoami", map[string]interface{}{})
	if !ok {
		t.Fatal("RPC request failed")
	}

	resultStr, ok := result.(string)
	if !ok {
		t.Fatalf("Unexpected result type: %T", result)
	}
	expected := "Hello from serverNode"
	if resultStr != expected {
		t.Fatalf("RPC result mismatch. Got: %s, Expected: %s", resultStr, expected)
	}

	t.Logf("RPC call successful. Result: %s", resultStr)
}

func TestWebSocketRpc(t *testing.T) {
	node := NewNode("ws-node")
	server := NewServer(node)

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", server.handleWS)
		if err := r.Run(":18080"); err != nil {
			t.Errorf("Gin server failed: %v", err)
		}
	}()
	time.Sleep(time.Second)

	client := NewMajulaClient("http://localhost:18080", "client-A")

	done := make(chan struct{})
	client.RegisterRpc("echo", func(fun string, args map[string]interface{}) interface{} {
		text, _ := args["text"].(string)
		result := "Echo: " + text
		close(done)
		return result
	}, nil)

	time.Sleep(1 * time.Second)

	go func() {
		time.Sleep(500 * time.Millisecond)
		err := server.SendToClient("client-A", MajulaPackage{
			Method:   "RPC",
			Fun:      "echo",
			Args:     map[string]interface{}{"text": "hello"},
			InvokeId: 12345,
		})
		if err != nil {
			t.Errorf("SendToClient failed: %v", err)
		}
	}()

	select {
	case <-done:
		t.Log("RPC echo was triggered successfully via WebSocket")
	case <-time.After(2 * time.Second):
		t.Fatal("RPC not triggered")
	}
}

func TestWebSocketRpcCallToNodeRegisteredService(t *testing.T) {
	node := NewNode("test-node")
	server := NewServer(node)

	node.registerRpcService("add", "default", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		a, _ := params["a"].(float64)
		b, _ := params["b"].(float64)
		return map[string]interface{}{"sum": a + b}
	})

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", server.handleWS)
		err := r.Run(":18080")
		if err != nil {
			t.Errorf("Gin server failed: %v", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	client := NewMajulaClient("http://localhost:18080", "client-A")
	time.Sleep(2 * time.Second)

	time.Sleep(500 * time.Millisecond)

	args := map[string]interface{}{
		"a": 10,
		"b": 20,
	}

	result, ok := client.CallRpc("add", "test-node", "default", args, 10*time.Second)
	if !ok {
		t.Fatal("RPC call failed")
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Invalid response format: %+v", result)
	}

	sum, ok := resMap["sum"].(float64)
	if !ok || sum != 30 {
		t.Fatalf("Unexpected result. Got: %+v", resMap)
	}

	t.Logf("RPC call to 'add' succeeded. Sum: %v", sum)
}

func TestCallAllRpcsViaWs(t *testing.T) {
	serverNode := NewNode("serverNode")

	worker := NewTcpConnection(
		"serverNode", false, ":9000", "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec,
		nil, defaultToken,
	)
	if worker == nil {
		t.Fatal("Failed to create TCP server")
	}
	channel := NewChannelFull("server-channel", serverNode, worker)
	worker.User = channel
	serverNode.addChannel(channel)
	serverNode.register()

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", NewServer(serverNode).handleWS)
		if err := r.Run(":18080"); err != nil {
			t.Errorf("WebSocket server failed: %v", err)
		}
	}()
	t.Log("Server node + WebSocket started")
	time.Sleep(1 * time.Second)

	clientNode := NewNode("clientNode")

	clientWorker := NewTcpConnection(
		"clientNode", true, "", "127.0.0.1:9000", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec,
		nil, defaultToken,
	)
	if clientWorker == nil {
		t.Fatal("Failed to connect clientNode to server")
	}
	clientChannel := NewChannelFull("client-channel", clientNode, clientWorker)
	clientWorker.User = clientChannel
	clientChannel.addChannelPeer("serverNode")
	clientNode.addChannel(clientChannel)
	clientNode.register()

	t.Log("Client node connected to server")

	wsClient := NewMajulaClient("http://localhost:18080", "tester-ws")
	time.Sleep(2 * time.Second) // 等待连接稳定

	params := map[string]interface{}{"rpcProvider": "init"}
	res, ok := wsClient.CallRpc("allrpcs", "clientNode", "init", params, 5*time.Second)
	if !ok {
		t.Fatal("RPC call to allrpcs failed")
	}

	list, ok := res.([]interface{})
	if !ok {
		t.Fatalf("Unexpected response: %+v", res)
	}

	if len(list) == 0 {
		t.Error("Empty result from allrpcs")
	} else {
		t.Logf("allrpcs returned %d functions:", len(list))
		for _, entry := range list {
			if m, ok := entry.(map[string]interface{}); ok {
				t.Logf("  - %s: %s", m["name"], m["note"])
			}
		}
	}
}

func TestFrpCommunicationBetweenNodes(t *testing.T) {

	netConnectionAddr := "127.0.0.1:3000"

	frpClientAddr := "127.0.0.1:23333"
	frpServerAddr := "127.0.0.1:23337"

	serverNode := NewNode("server")
	clientA := NewNode("clientA")
	clientB := NewNode("clientB")

	serverWorker := NewTcpConnection("server", false, netConnectionAddr, "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	serverChannel := NewChannelFull("serverChan", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.addChannel(serverChannel)
	serverNode.register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	clientAChannel.addChannelPeer("server")
	clientA.addChannel(clientAChannel)
	clientA.register()

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	clientBChannel.addChannelPeer("server")
	clientB.addChannel(clientBChannel)
	clientB.register()

	time.Sleep(2 * time.Second)

	code := "test-frp"

	err := clientA.stubManager.RegisterFRPCode(code, frpClientAddr, "clientB", frpServerAddr)
	if err != nil {
		t.Fatal("Failed to register FRP on clientA:", err)
	}
	err = clientB.stubManager.RegisterFRPCode(code, frpClientAddr, "clientA", frpServerAddr)
	if err != nil {
		t.Fatal("Failed to register FRP on clientB:", err)
	}

	go func() {
		ln, err := net.Listen("tcp", frpServerAddr)
		if err != nil {
			t.Fatalf("Failed to start listener: %v", err)
		}
		defer ln.Close()
		fmt.Println("Before tcp server accept")
		conn, err := ln.Accept()
		fmt.Println("Succeed in tcp server accept")
		if err != nil {
			t.Fatalf("Failed to accept: %v", err)
		}
		defer conn.Close()

		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			t.Logf("ClientB received: %s", string(buf[:n]))
		}

	}()

	time.Sleep(1 * time.Second)

	if err := clientA.stubManager.StartFRPListener(code, frpClientAddr); err != nil {
		t.Fatalf("ClientA StartFRPListener failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	go func() {
		conn, err := net.Dial("tcp", frpClientAddr)
		if err != nil {
			t.Fatalf("Dial on port 23333 failed: %v", err)
			conn.Close()
		}
		for i := 0; i < 50; i++ {
			conn.Write([]byte(fmt.Sprintf("msg-%d", i)))
			time.Sleep(500 * time.Millisecond)
		}
		clientA.stubManager.CloseAllStubs()
	}()

	/*
		if err := clientA.stubManager.RunFRPFromLocalStub(code); err != nil {
			t.Fatalf("ClientA RunFRP failed: %v", err)
		}
		if err := clientB.stubManager.StartFRPListener(code, "localhost:9002"); err != nil {
			t.Fatalf("ClientB StartFRPListener failed: %v", err)
		}

	*/

	time.Sleep(5 * time.Second)
}
