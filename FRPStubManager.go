package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type FRPConfig struct {
	Code         string
	LocalTarget  string
	RemoteNodeID string
	PeerTarget   string
}

type StubManager struct {
	node        *Node
	myNodeId    string
	stubs       map[string]*StreamStub
	stubMutex   sync.Mutex
	stubCounter int64

	frpConfigs     map[string]*FRPConfig
	frpConfigMutex sync.RWMutex
}

func (sm *StubManager) RegisterFRP(config *FRPConfig) error {
	sm.frpConfigMutex.Lock()
	defer sm.frpConfigMutex.Unlock()

	if sm.frpConfigs == nil {
		sm.frpConfigs = make(map[string]*FRPConfig)
	}
	sm.frpConfigs[config.Code] = config

	return nil
}

func NewStubManager(node *Node, myNodeId string) *StubManager {
	return &StubManager{
		node:     node,
		myNodeId: myNodeId,
		stubs:    make(map[string]*StreamStub),
	}
}

func (this *Node) initStubManager() {
	this.stubManager = NewStubManager(this, this.ID)
}

func (sm *StubManager) unregisterStub(stubId string) {
	sm.stubMutex.Lock()
	defer sm.stubMutex.Unlock()

	if stub, exists := sm.stubs[stubId]; exists {
		if stub.conn != nil {
			fmt.Println("conn close due to unregister stub")
			stub.conn.Close()
		}
		if stub.cancel != nil {
			stub.cancel()
		}
		delete(sm.stubs, stubId)
	}
}

func (sm *StubManager) GetStubById(stubId string) (*StreamStub, bool) {
	sm.stubMutex.Lock()
	defer sm.stubMutex.Unlock()
	stub, exists := sm.stubs[stubId]
	return stub, exists
}

func (sm *StubManager) CloseAllStubs() {
	sm.stubMutex.Lock()
	defer sm.stubMutex.Unlock()

	for id, stub := range sm.stubs {
		if stub.conn != nil {
			fmt.Println("conn close due to unregister all stubs")
			stub.conn.Close()
		}
		if stub.cancel != nil {
			stub.cancel()
		}
		delete(sm.stubs, id)
	}
}

func (node *Node) ConnectTcpHandler(dst string, peerStubId string) (string, error) {
	localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.stubManager.stubCounter, 1))

	conn, err := net.Dial("tcp", dst)
	if err != nil {
		return "", fmt.Errorf("failed to connect to target %s: %v", dst, err)
	}

	stub := NewStreamStub(node, conn, localStubId, "", peerStubId, node.ID)

	node.stubManager.stubMutex.Lock()
	node.stubManager.stubs[localStubId] = stub
	node.stubManager.stubMutex.Unlock()

	stub.startSendLoop()
	stub.startRecvLoop()

	return localStubId, nil
}

func (node *Node) ConnectTcpRpcWrapper() {
	node.registerRpcService("_connect_tcp", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		dst, ok := params["dst"].(string)
		if !ok {
			return map[string]interface{}{"error": "invalid or missing 'dst'"}
		}
		peerStubId, ok := params["stub_id"].(string)
		if !ok {
			return map[string]interface{}{"error": "invalid or missing 'stub_id'"}
		}

		localStubId, err := node.ConnectTcpHandler(dst, peerStubId)
		if err != nil {
			return map[string]interface{}{"error": err.Error()}
		}

		return map[string]interface{}{
			"ok":           true,
			"peer_stub_id": localStubId,
		}
	})
}

func (sm *StubManager) RunFRPFromLocalStub(code string) error {
	sm.frpConfigMutex.RLock()
	config, ok := sm.frpConfigs[code]
	sm.frpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	conn, err := net.Dial("tcp", config.LocalTarget)
	if err != nil {
		return fmt.Errorf("failed to connect to local target %s: %v", config.LocalTarget, err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.stubCounter, 1))
	stub := NewStreamStub(sm.node, conn, stubId, config.RemoteNodeID, "", sm.myNodeId)

	sm.stubMutex.Lock()
	sm.stubs[stubId] = stub
	sm.stubMutex.Unlock()

	params := map[string]interface{}{
		"code":    code,
		"stub_id": stubId,
	}
	result, ok := sm.node.makeRpcRequest(config.RemoteNodeID, "init", "_frp_connect", params)
	if !ok || result == nil {
		fmt.Println("conn close due error in rpc request")
		conn.Close()
		sm.unregisterStub(stubId)
		return fmt.Errorf("RPC _frp_connect failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		fmt.Println("conn close due to error in rpc request return structure")
		conn.Close()
		sm.unregisterStub(stubId)
		return fmt.Errorf("unexpected RPC response format")
	}

	peerStubVal, exists := resultMap["peer_stub_id"]
	if !exists {
		fmt.Println("conn close due to no peer stub id in rpc response")
		conn.Close()
		sm.unregisterStub(stubId)
		return fmt.Errorf("RPC response missing 'peer_stub_id'")
	}
	peerStubId, ok := peerStubVal.(string)
	if !ok || peerStubId == "" {
		fmt.Println("conn close due to peer stub id in rpc response is wrongly set")
		conn.Close()
		sm.unregisterStub(stubId)
		return fmt.Errorf("invalid 'peer_stub_id' in RPC response")
	}

	stub.peerStubId = peerStubId

	stub.startSendLoop()
	stub.startRecvLoop()

	return nil
}

func (node *Node) RegisterFRPRPCHandler() {
	node.registerRpcService("_frp_connect", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		code, ok := params["code"].(string)
		if !ok || code == "" {
			return map[string]interface{}{"error": "missing or invalid 'code'"}
		}
		peerStubId, ok := params["stub_id"].(string)
		if !ok || peerStubId == "" {
			return map[string]interface{}{"error": "missing or invalid 'stub_id'"}
		}

		node.stubManager.frpConfigMutex.RLock()
		config, exists := node.stubManager.frpConfigs[code]
		node.stubManager.frpConfigMutex.RUnlock()
		if !exists {
			return map[string]interface{}{"error": fmt.Sprintf("FRP code '%s' not registered", code)}
		}
		fmt.Println("___________________----------------- The tcp server dial addr is " + config.PeerTarget)
		conn, err := net.Dial("tcp", config.PeerTarget)

		if err != nil {
			return map[string]interface{}{"error": fmt.Sprintf("failed to connect to PeerTarget: %v", err)}
		}

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.stubManager.stubCounter, 1))
		stub := NewStreamStub(node, conn, localStubId, from, peerStubId, node.ID)

		node.stubManager.stubMutex.Lock()
		node.stubManager.stubs[localStubId] = stub
		node.stubManager.stubMutex.Unlock()

		stub.startSendLoop()
		stub.startRecvLoop()

		return map[string]interface{}{
			"peer_stub_id": localStubId,
		}
	})
}

func (sm *StubManager) StartFRPListener(code string, listenAddr string) error {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", listenAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Println("FRP listener accept error:", err)
				continue
			}
			go func(clientConn net.Conn) {
				/*
					defer func() {
						fmt.Println("------------Seems the defer cause the conn to close")
						clientConn.Close()
					}()

				*/

				stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.stubCounter, 1))
				sm.frpConfigMutex.RLock()
				config, ok := sm.frpConfigs[code]
				sm.frpConfigMutex.RUnlock()
				if !ok {
					fmt.Printf("FRP code '%s' not registered\n", code)
					fmt.Println("conn close due to wrong rpc code")
					clientConn.Close()
					return
				}

				remoteNodeId := config.RemoteNodeID
				stub := NewStreamStub(sm.node, clientConn, stubId, remoteNodeId, "", sm.myNodeId)

				sm.stubMutex.Lock()
				sm.stubs[stubId] = stub
				sm.stubMutex.Unlock()

				if err := sm.runFRPWithExistingConn(code, stubId, clientConn); err != nil {
					fmt.Printf("FRP dynamic tunnel failed: %v\n", err)
					sm.unregisterStub(stubId)
					return
				}

				stub.startSendLoop()
				stub.startRecvLoop()
				fmt.Println("-----------Reach the end of the connection establish------------")
			}(conn)
		}
	}()

	fmt.Println("Started FRP listener on", listenAddr, "for code", code)
	return nil
}

func (sm *StubManager) runFRPWithExistingConn(code, stubId string, conn net.Conn) error {
	sm.frpConfigMutex.RLock()
	config, ok := sm.frpConfigs[code]
	sm.frpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	params := map[string]interface{}{
		"code":    code,
		"stub_id": stubId,
	}
	result, ok := sm.node.makeRpcRequest(config.RemoteNodeID, "init", "_frp_connect", params)
	if !ok || result == nil {
		return fmt.Errorf("RPC _frp_connect failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Errorf("unexpected RPC response format")
	}
	fmt.Println("_______________________________________________________")
	fmt.Println(resultMap)

	peerStubVal, exists := resultMap["peer_stub_id"]
	fmt.Println("The peer id got +" + peerStubVal.(string))
	if !exists {
		return fmt.Errorf("RPC response missing 'peer_stub_id'")
	}
	peerStubId, ok := peerStubVal.(string)
	if !ok || peerStubId == "" {
		return fmt.Errorf("invalid 'peer_stub_id' in RPC response")
	}

	sm.stubMutex.Lock()
	if stub, ok := sm.stubs[stubId]; ok {
		stub.peerStubId = peerStubId
	}
	sm.stubMutex.Unlock()

	return nil
}

type fileConnAdapter struct {
	*os.File
}

func (f *fileConnAdapter) SetReadDeadline(t time.Time) error  { return nil }
func (f *fileConnAdapter) SetWriteDeadline(t time.Time) error { return nil }
func (f *fileConnAdapter) SetDeadline(t time.Time) error      { return nil }

func (f *fileConnAdapter) LocalAddr() net.Addr {
	return dummyAddr("file-local")
}

func (f *fileConnAdapter) RemoteAddr() net.Addr {
	return dummyAddr("file-remote")
}

type dummyAddr string

func (d dummyAddr) Network() string { return "file" }
func (d dummyAddr) String() string  { return string(d) }

func (sm *StubManager) TransferFileToRemote(code, localFilePath, remoteFilePath string) error {
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.stubCounter, 1))

	sm.frpConfigMutex.RLock()
	config, ok := sm.frpConfigs[code]
	sm.frpConfigMutex.RUnlock()
	if !ok {
		file.Close()
		return fmt.Errorf("FRP code '%s' not registered", code)
	}
	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.node, wrappedFile, stubId, config.RemoteNodeID, "", sm.myNodeId)

	sm.stubMutex.Lock()
	sm.stubs[stubId] = stub
	sm.stubMutex.Unlock()

	params := map[string]interface{}{
		"code":     code,
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "w",
	}
	result, ok := sm.node.makeRpcRequest(config.RemoteNodeID, "init", "_open_file", params)
	if !ok {
		return fmt.Errorf("RPC _open_file failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}
	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.startSendLoop()

	return nil
}

func (node *Node) RegisterFileTransferRPCs() {
	node.registerRpcService("_open_file", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		code, _ := params["code"].(string)
		stubId, _ := params["stub_id"].(string)
		filename, _ := params["filename"].(string)
		mode, _ := params["mode"].(string)

		node.stubManager.frpConfigMutex.RLock()
		_, exists := node.stubManager.frpConfigs[code]
		node.stubManager.frpConfigMutex.RUnlock()
		if !exists {
			return map[string]interface{}{"error": fmt.Sprintf("FRP code '%s' not registered", code)}
		}

		if filename == "" || stubId == "" || (mode != "r" && mode != "w") {
			return map[string]interface{}{"error": "invalid parameters"}
		}

		var file *os.File
		var err error
		if mode == "r" {
			file, err = os.Open(filename)
		} else {
			file, err = os.Create(filename)
		}
		if err != nil {
			return map[string]interface{}{"error": fmt.Sprintf("file open error: %v", err)}
		}

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.stubManager.stubCounter, 1))
		wrappedFile := &fileConnAdapter{File: file}
		stub := NewStreamStub(node, wrappedFile, localStubId, from, stubId, node.ID)

		node.stubManager.stubMutex.Lock()
		node.stubManager.stubs[localStubId] = stub
		node.stubManager.stubMutex.Unlock()

		if mode == "r" {
			stub.startSendLoop()
		} else {
			stub.startRecvLoop()
		}

		return map[string]interface{}{"ok": true, "peer_stub_id": localStubId}
	})

	node.registerRpcService("_close_file", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		stubId, _ := params["stub_id"].(string)
		if stubId == "" {
			return map[string]interface{}{"error": "missing stub_id"}
		}
		node.stubManager.unregisterStub(stubId)
		return map[string]interface{}{"ok": true}
	})

}

func (sm *StubManager) DownloadFileFromRemote(code, remoteFilePath, localFilePath string) error {
	file, err := os.Create(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.stubCounter, 1))

	sm.frpConfigMutex.RLock()
	config, ok := sm.frpConfigs[code]
	sm.frpConfigMutex.RUnlock()
	if !ok {
		file.Close()
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.node, wrappedFile, stubId, config.RemoteNodeID, "", sm.myNodeId)

	sm.stubMutex.Lock()
	sm.stubs[stubId] = stub
	sm.stubMutex.Unlock()

	params := map[string]interface{}{
		"code":     code,
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "r",
	}
	result, ok := sm.node.makeRpcRequest(config.RemoteNodeID, "init", "_open_file", params)
	if !ok {
		return fmt.Errorf("RPC _open_file failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}

	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.startRecvLoop()

	return nil
}

func (sm *StubManager) handleFrpMessages(msg *Message) {
	var stubId string

	switch msg.Type {
	case FRPData:
		var payload FRPDataPayload
		if err := json.Unmarshal([]byte(msg.Data), &payload); err != nil {
			fmt.Println("FRPData unmarshal error:", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPAck:
		var payload FRPAckPayload
		if err := json.Unmarshal([]byte(msg.Data), &payload); err != nil {
			fmt.Println("FRPAck unmarshal error:", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPResendRequest:
		var payload FRPResendRequestPayload
		if err := json.Unmarshal([]byte(msg.Data), &payload); err != nil {
			fmt.Println("FRPResendRequest unmarshal error:", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPClose:
		stubId = msg.Data

	default:
		fmt.Println("Unhandled FRP message type:", msg.Type)
		return
	}

	stub, ok := sm.GetStubById(stubId)
	if !ok {
		fmt.Printf("handleFrpMessages: stub not found for %s (type %v)\n", stubId, msg.Type)
		return
	}

	select {
	case stub.inChan <- frpMessage{msgType: msg.Type, payload: []byte(msg.Data)}:
	default:
		fmt.Printf("handleFrpMessages: inChan full for stub %s (dropping message)\n", stubId)
	}
}

func (sm *StubManager) RegisterFRPCode(code, localTarget, remoteNodeId, peerTarget string) error {
	if code == "" || localTarget == "" || remoteNodeId == "" || peerTarget == "" {
		return fmt.Errorf("all fields must be non-empty")
	}
	config := &FRPConfig{
		Code:         code,
		LocalTarget:  localTarget,
		RemoteNodeID: remoteNodeId,
		PeerTarget:   peerTarget,
	}
	return sm.RegisterFRP(config)
}
