package core

import (
	"Majula/api"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	defaultMaxFrameSize         = 4096
	defaultMaxInactiveSeconds   = int64(10)
	defaultMaxSendQueueSize     = 1000
	defaultMaxConnectionsPerSec = 5
	defaultToken                = "default_token"
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

	// Setup TCP Nodes
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
	node := NewNode("test-Node")
	server := NewServer(node, "18080")

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

	registerMsg := api.MajulaPackage{
		Method: "REGISTER_CLIENT",
	}
	msgBytes, _ := json.Marshal(registerMsg)
	err = conn.WriteMessage(websocket.TextMessage, msgBytes)
	if err != nil {
		t.Fatalf("Failed to send Register message: %v", err)
	}

	time.AfterFunc(500*time.Millisecond, func() {
		server.SendToClient("test-client", api.MajulaPackage{
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
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

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
	clientNode.AddChannel(clientChannel)
	clientNode.Register()

	// 3. 等待两边连接初始化、路由表生成
	time.Sleep(2 * time.Second)

	result, ok := clientNode.MakeRpcRequest("serverNode", "default", "whoami", map[string]interface{}{})
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
	node := NewNode("ws-Node")
	server := NewServer(node, "18080")

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", server.handleWS)
		if err := r.Run(":18080"); err != nil {
			t.Errorf("Gin server failed: %v", err)
		}
	}()
	time.Sleep(time.Second)

	client := api.NewMajulaClient("http://localhost:18080", "client-A")

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
		err := server.SendToClient("client-A", api.MajulaPackage{
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
	node := NewNode("test-Node")
	server := NewServer(node, "18080")

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

	client := api.NewMajulaClient("http://localhost:18080", "client-A")
	time.Sleep(2 * time.Second)

	time.Sleep(500 * time.Millisecond)

	args := map[string]interface{}{
		"a": 10,
		"b": 20,
	}

	result, ok := client.CallRpc("add", "test-Node", "default", args, 10*time.Second)
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
	serverNode.AddChannel(channel)
	serverNode.Register()

	go func() {
		r := gin.Default()
		r.GET("/ws/:target", NewServer(serverNode, "18080").handleWS)
		if err := r.Run(":18080"); err != nil {
			t.Errorf("WebSocket server failed: %v", err)
		}
	}()
	t.Log("Server Node + WebSocket started")
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
	clientNode.AddChannel(clientChannel)
	clientNode.Register()

	t.Log("Client Node connected to server")

	wsClient := api.NewMajulaClient("http://localhost:18080", "tester-ws")
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
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	clientAChannel.addChannelPeer("server")
	clientA.AddChannel(clientAChannel)
	clientA.Register()

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	clientBChannel.addChannelPeer("server")
	clientB.AddChannel(clientBChannel)
	clientB.Register()

	time.Sleep(2 * time.Second)

	code := "test-frp"

	err := clientA.StubManager.RegisterFRPWithCode(code, frpClientAddr, "clientB", frpServerAddr)
	if err != nil {
		t.Fatal("Failed to Register FRP on clientA:", err)
	}
	err = clientB.StubManager.RegisterFRPWithCode(code, frpClientAddr, "clientA", frpServerAddr)
	if err != nil {
		t.Fatal("Failed to Register FRP on clientB:", err)
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

		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			t.Logf("ClientB received: %s", string(buf[:n]))
		}

	}()

	time.Sleep(1 * time.Second)

	if err := clientA.StubManager.RunRegisteredFRP(code); err != nil {
		t.Fatalf("ClientA RunRegisteredFRP failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	go func() {
		conn, err := net.Dial("tcp", frpClientAddr)
		if err != nil {
			t.Fatalf("Dial on Port 23333 failed: %v", err)
			return
		}
		defer conn.Close()

		time.Sleep(1 * time.Second)

		for i := 0; i < 15000; i++ {
			message := []byte(fmt.Sprintf("msg-%d", i))

			for {
				_, err := conn.Write(message)
				if err == nil {
					break
				}

				time.Sleep(10 * time.Millisecond)
			}
			//t.Logf("ClientA send: %s", "msg-"+fmt.Sprintf("%d", i))
		}
	}()

	/*
		if err := clientA.StubManager.RunFRPWithStub(code); err != nil {
			t.Fatalf("ClientA RunFRP failed: %v", err)
		}
		if err := clientB.StubManager.RunRegisteredFRP(code, "localhost:9002"); err != nil {
			t.Fatalf("ClientB RunRegisteredFRP failed: %v", err)
		}

	*/

	time.Sleep(5 * time.Second)
	clientA.StubManager.CloseAllStubs()
}

func TestWindowBufferAdvance(t *testing.T) {
	const size = 1024
	wb := NewWindowBuffer(1, size)

	for i := int64(1); i <= int64(size); i++ {
		ok := wb.Put(i, []byte{byte(i % 256)})
		if !ok {
			t.Fatalf("Put failed at seq %d", i)
		}
	}

	for i := int64(1); i <= int64(size); i++ {
		data, ok := wb.Get(i)
		if !ok || data[0] != byte(i%256) {
			t.Errorf("Get mismatch at seq %d: got %v, ok=%v", i, data, ok)
		}
	}

	ok := wb.Put(int64(size)+1, []byte{0})
	if ok {
		t.Errorf("Expected Put to fail when window is full")
	}

	advanced := wb.BundleAdvanceUpTo(int64(size))
	if advanced != size {
		t.Errorf("Expected to advance %d slots, got %d", size, advanced)
	}

	if wb.Count() != 0 {
		t.Errorf("Expected Count = 0 after full advance, got %d", wb.Count())
	}

	for i := int64(size + 1); i <= int64(size*2); i++ {
		ok := wb.Put(i, []byte{byte(i % 256)})
		if !ok {
			t.Fatalf("Put failed after advance at seq %d", i)
		}
	}
}

func TestFrpFileTransfer(t *testing.T) {

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
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	clientAChannel.addChannelPeer("server")
	clientA.AddChannel(clientAChannel)
	clientA.Register()

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	clientBChannel.addChannelPeer("server")
	clientB.AddChannel(clientBChannel)
	clientB.Register()

	time.Sleep(2 * time.Second)

	code := "test-frp-file"

	if err := clientA.StubManager.RegisterFRPWithCode(code, frpClientAddr, "clientB", frpServerAddr); err != nil {
		t.Fatalf("ClientA Register FRP failed: %v", err)
	}
	if err := clientB.StubManager.RegisterFRPWithCode(code, frpClientAddr, "clientA", frpServerAddr); err != nil {
		t.Fatalf("ClientB Register FRP failed: %v", err)
	}

	srcFile := "test_input.txt"
	dstFile := "test_output.txt"
	content := "Hello FRP file transfer!"
	err := os.WriteFile(srcFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to write test input file: %v", err)
	}
	defer os.Remove(srcFile)
	defer os.Remove(dstFile)

	time.Sleep(1 * time.Second)

	err = clientA.StubManager.RegisteredTransferFileToRemote(code, srcFile, dstFile)
	if err != nil {
		t.Fatalf("RegisteredTransferFileToRemote failed: %v", err)
	}

	time.Sleep(2 * time.Second)

	data, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}

	if string(data) != content {
		t.Fatalf("File content mismatch. Got: %s, Expected: %s", string(data), content)
	}
}

func TestFrpDynamicTunnel(t *testing.T) {
	netConnectionAddr := "127.0.0.1:3002"
	dynClientAddr := "127.0.0.1:24444"
	dynServerAddr := "127.0.0.1:24445"

	serverNode := NewNode("server")
	clientA := NewNode("clientA")
	clientB := NewNode("clientB")

	serverWorker := NewTcpConnection("server", false, netConnectionAddr, "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	serverChannel := NewChannelFull("serverChan", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	//clientAChannel.addChannelPeer("server")
	clientA.AddChannel(clientAChannel)
	clientA.Register()

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	//clientBChannel.addChannelPeer("server")
	clientB.AddChannel(clientBChannel)
	clientB.Register()

	time.Sleep(8 * time.Second)
	fmt.Println("[DEBUG] serverNode 路由表：")
	serverNode.printRoutingTable()
	fmt.Println("[DEBUG] clientA 路由表：")
	clientA.printRoutingTable()
	fmt.Println("[DEBUG] clientB 路由表：")
	clientB.printRoutingTable()
	time.Sleep(1 * time.Second)

	if err := clientA.StubManager.RegisterFRPAndRun("clientB", dynClientAddr, dynServerAddr); err != nil {
		t.Fatalf("RegisterFRPAndRun failed: %v", err)
	}

	go func() {
		ln, err := net.Listen("tcp", dynServerAddr)
		if err != nil {
			t.Fatalf("Server listen failed: %v", err)
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			t.Fatalf("Server accept failed: %v", err)
		}
		defer conn.Close()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			t.Logf("Server received: %s", string(buf[:n]))
		}
	}()

	time.Sleep(1 * time.Second)

	go func() {
		conn, err := net.Dial("tcp", dynClientAddr)
		if err != nil {
			t.Fatalf("Client dial failed: %v", err)
		}
		defer conn.Close()
		time.Sleep(1 * time.Second)
		for i := 0; i < 2000; i++ {
			msg := fmt.Sprintf("ping-%d", i)
			conn.Write([]byte(msg))
			time.Sleep(1 * time.Millisecond)
		}
	}()

	time.Sleep(10 * time.Second)
	clientA.StubManager.CloseAllStubs()
}

func TestFrpDynamicFileTransfer(t *testing.T) {
	netConnectionAddr := "127.0.0.1:3001"

	clientA := NewNode("clientA")
	clientB := NewNode("clientB")

	serverWorker := NewTcpConnection("server", false, netConnectionAddr, "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	serverNode := NewNode("server")
	serverChannel := NewChannelFull("serverChan", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	clientAChannel.addChannelPeer("server")
	clientA.AddChannel(clientAChannel)
	clientA.Register()

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	clientBChannel.addChannelPeer("server")
	clientB.AddChannel(clientBChannel)
	clientB.Register()

	time.Sleep(2 * time.Second)

	srcFile := "test_input_dyn.txt"
	dstFile := "test_output_dyn.txt"
	content := "Dynamic FRP file transfer test"
	err := os.WriteFile(srcFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to write test input file: %v", err)
	}
	defer os.Remove(srcFile)
	defer os.Remove(dstFile)

	err = clientA.StubManager.TransferFileToRemoteWithoutRegistration("clientB", srcFile, dstFile)
	if err != nil {
		t.Fatalf("TransferFileToRemoteWithoutRegistration failed: %v", err)
	}

	time.Sleep(2 * time.Second)

	data, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}

	if string(data) != content {
		t.Fatalf("File content mismatch. Got: %s, Expected: %s", string(data), content)
	}
}

func TestFrpDynamicTunnelWithClient(t *testing.T) {
	netConnectionAddr := "127.0.0.1:3002"
	dynClientAddr := "127.0.0.1:24444"
	dynServerAddr := "127.0.0.1:24445"

	serverNode := NewNode("server")
	clientA := NewNode("clientA")
	clientB := NewNode("clientB")

	serverWorker := NewTcpConnection("server", false, netConnectionAddr, "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	serverChannel := NewChannelFull("serverChan", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.AddChannel(serverChannel)
	serverNode.Register()

	clientAWorker := NewTcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	//clientAChannel.addChannelPeer("server")
	clientA.AddChannel(clientAChannel)
	clientA.Register()
	s := NewServer(clientA, "23333")

	// 在后台启动服务器
	go func() {
		t.Logf("启动MajulaServer在端口: %s", s.Port)
		SetupRoutes(s).Run(":" + s.Port)
	}()

	// 等待服务器启动
	time.Sleep(3 * time.Second)

	clientBWorker := NewTcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, nil, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	//clientBChannel.addChannelPeer("server")
	clientB.AddChannel(clientBChannel)
	clientB.Register()

	time.Sleep(2 * time.Second)
	client1 := api.NewMajulaClient("http://127.0.0.1:23333", "client1")

	// 等待WebSocket连接建立，添加详细日志
	t.Logf("开始等待WebSocket连接建立...")
	deadline := time.Now().Add(15 * time.Second) // 增加超时时间
	checkCount := 0
	for time.Now().Before(deadline) {
		checkCount++
		if client1.Connected {
			t.Logf("WebSocket连接已建立: %s (检查次数: %d)", client1.Entity, checkCount)
			break
		}
		if checkCount%10 == 0 { // 每1秒输出一次状态
			t.Logf("WebSocket连接状态检查中... (检查次数: %d, Connected: %v)", checkCount, client1.Connected)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !client1.Connected {
		t.Fatalf("WebSocket连接超时: %s (检查次数: %d)", client1.Entity, checkCount)
	}

	// 注意：mainLoop中已经自动调用了RegisterClientID，这里不需要重复调用
	t.Logf("WebSocket连接已建立，客户端ID已自动注册")
	go func() {
		ln, err := net.Listen("tcp", dynServerAddr)
		if err != nil {
			t.Fatalf("Server listen failed: %v", err)
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			t.Fatalf("Server accept failed: %v", err)
		}
		defer conn.Close()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			t.Logf("Server received: %s", string(buf[:n]))
		}
	}()
	time.Sleep(1 * time.Second)
	client1.StartFRPWithoutRegistration(dynClientAddr, "clientB", dynServerAddr)

	/*
		if err := clientA.StubManager.RegisterFRPAndRun("clientB", dynClientAddr, dynServerAddr); err != nil {
			t.Fatalf("RegisterFRPAndRun failed: %v", err)
		}

	*/

	time.Sleep(1 * time.Second)

	go func() {
		conn, err := net.Dial("tcp", dynClientAddr)
		if err != nil {
			t.Fatalf("Client dial failed: %v", err)
		}
		defer conn.Close()
		time.Sleep(1 * time.Second)
		for i := 0; i < 15000; i++ {
			msg := fmt.Sprintf("ping-%d", i)
			conn.Write([]byte(msg))
			time.Sleep(1 * time.Millisecond)
		}
	}()

	time.Sleep(5 * time.Second)
	clientA.StubManager.CloseAllStubs()
}

func TestKcpDynamicTunnel(t *testing.T) {
	netConnectionAddr := "127.0.0.1:3002"
	dynClientAddr := "127.0.0.1:24444"
	dynServerAddr := "127.0.0.1:24445"

	//t.Logf("[TEST] Creating serverNode: server")
	serverNode := NewNode("server")
	//t.Logf("[TEST] Creating clientA: clientA")
	clientA := NewNode("clientA")
	//t.Logf("[TEST] Creating clientB: clientB")
	clientB := NewNode("clientB")

	//t.Logf("[TEST] Creating serverWorker (KCP) on %s", netConnectionAddr)
	serverWorker := NewKcpConnection("server", false, netConnectionAddr, "", nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, defaultToken)
	serverChannel := NewChannelFull("serverChan", serverNode, serverWorker)
	serverWorker.User = serverChannel
	serverNode.AddChannel(serverChannel)
	serverNode.Register()
	//t.Logf("[TEST] serverNode registered")

	//t.Logf("[TEST] Creating clientAWorker (KCP) connect to %s", netConnectionAddr)
	clientAWorker := NewKcpConnection("clientA", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, defaultToken)
	clientAChannel := NewChannelFull("chanA", clientA, clientAWorker)
	clientAWorker.User = clientAChannel
	clientA.AddChannel(clientAChannel)
	clientA.Register()
	//t.Logf("[TEST] clientA registered")

	//t.Logf("[TEST] Creating clientBWorker (KCP) connect to %s", netConnectionAddr)
	clientBWorker := NewKcpConnection("clientB", true, "", netConnectionAddr, nil,
		defaultMaxFrameSize, defaultMaxInactiveSeconds,
		defaultMaxSendQueueSize, defaultMaxConnectionsPerSec, defaultToken)
	clientBChannel := NewChannelFull("chanB", clientB, clientBWorker)
	clientBWorker.User = clientBChannel
	clientB.AddChannel(clientBChannel)
	clientB.Register()
	//t.Logf("[TEST] clientB registered")

	//t.Logf("[TEST] Sleep 2s for network stabilization")
	time.Sleep(8 * time.Second)
	fmt.Println("[DEBUG] serverNode 路由表：")
	serverNode.printRoutingTable()
	fmt.Println("[DEBUG] clientA 路由表：")
	clientA.printRoutingTable()
	fmt.Println("[DEBUG] clientB 路由表：")
	clientB.printRoutingTable()
	time.Sleep(1 * time.Second)

	//t.Logf("[TEST] RegisterFRPAndRun: clientA -> clientB, dynClientAddr=%s, dynServerAddr=%s", dynClientAddr, dynServerAddr)
	if err := clientA.StubManager.RegisterFRPAndRun("clientB", dynClientAddr, dynServerAddr); err != nil {
		t.Fatalf("RegisterFRPAndRun failed: %v", err)
	}
	//t.Logf("[TEST] RegisterFRPAndRun success")

	go func() {
		t.Logf("[TEST] FRP server listen on %s", dynServerAddr)
		ln, err := net.Listen("tcp", dynServerAddr)
		if err != nil {
			t.Fatalf("Server listen failed: %v", err)
		}
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			t.Fatalf("Server accept failed: %v", err)
		}
		t.Logf("[TEST] FRP server accepted connection from %s", conn.RemoteAddr().String())
		defer conn.Close()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				t.Logf("[TEST] FRP server read error: %v", err)
				return
			}
			t.Logf("[TEST] Server received: %s", string(buf[:n]))
		}
	}()

	t.Logf("[TEST] Sleep 1s before client dial")
	time.Sleep(1 * time.Second)

	go func() {
		t.Logf("[TEST] FRP client dial %s", dynClientAddr)
		conn, err := net.Dial("tcp", dynClientAddr)
		if err != nil {
			t.Fatalf("Client dial failed: %v", err)
		}
		t.Logf("[TEST] FRP client connected to %s", dynClientAddr)
		defer conn.Close()
		time.Sleep(1 * time.Second)
		for i := 0; i < 2000; i++ {
			msg := fmt.Sprintf("ping-%d", i)
			conn.Write([]byte(msg))
			//t.Logf("[TEST] FRP client sent: %s", msg)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	//t.Logf("[TEST] Sleep 10s for data transfer")
	time.Sleep(5 * time.Second)
	clientA.StubManager.CloseAllStubs()
	//t.Logf("[TEST] Closed all stubs")
}
