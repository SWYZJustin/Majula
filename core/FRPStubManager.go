package core

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
	Code       string
	LocalAddr  string
	RemoteNode string
	RemoteAddr string
}

type StubManager struct {
	Node        *Node
	MyNodeId    string
	Stubs       map[string]*StreamStub
	StubMutex   sync.Mutex
	StubCounter int64

	FrpConfigs     map[string]*FRPConfig
	FrpConfigMutex sync.RWMutex
}

func (sm *StubManager) RegisterFRP(config *FRPConfig) error {
	sm.FrpConfigMutex.Lock()
	defer sm.FrpConfigMutex.Unlock()

	if sm.FrpConfigs == nil {
		sm.FrpConfigs = make(map[string]*FRPConfig)
	}
	sm.FrpConfigs[config.Code] = config

	return nil
}

func (sm *StubManager) RegisterFRPWithCode(code, localAddr, remoteNode, remoteAddr string) error {
	if code == "" || localAddr == "" || remoteNode == "" || remoteAddr == "" {
		return fmt.Errorf("all fields must be non-empty")
	}
	config := &FRPConfig{
		Code:       code,
		LocalAddr:  localAddr,
		RemoteNode: remoteNode,
		RemoteAddr: remoteAddr,
	}
	return sm.RegisterFRP(config)
}

func (sm *StubManager) RegisterFRPSimplified(code string, remoteNode string, targetAddr string, isServer bool) error {
	if code == "" || remoteNode == "" || targetAddr == "" {
		return fmt.Errorf("code, remoteNode, and targetAddr must be non-empty")
	}

	config := &FRPConfig{
		Code:       code,
		RemoteNode: remoteNode,
	}

	if isServer {
		config.RemoteAddr = targetAddr
	} else {
		config.LocalAddr = targetAddr
	}

	return sm.RegisterFRP(config)
}

func WrapFRPAddr(localAddr string) string {
	code := "_http_frp_" + localAddr
	return code
}

func (sm *StubManager) RegisterFRPWithoutCode(localAddr, remoteNode, remoteAddr string) (string, error) {
	if localAddr == "" || remoteNode == "" || remoteAddr == "" {
		return "", fmt.Errorf("localAddr, remoteNode, and remoteAddr must be non-empty")
	}

	sm.FrpConfigMutex.Lock()
	defer sm.FrpConfigMutex.Unlock()

	if sm.FrpConfigs == nil {
		sm.FrpConfigs = make(map[string]*FRPConfig)
	}

	code := WrapFRPAddr(localAddr)

	sm.FrpConfigs[code] = &FRPConfig{
		Code:       code,
		LocalAddr:  localAddr,
		RemoteNode: remoteNode,
		RemoteAddr: remoteAddr,
	}

	return code, nil
}

func NewStubManager(node *Node, myNodeId string) *StubManager {
	return &StubManager{
		Node:     node,
		MyNodeId: myNodeId,
		Stubs:    make(map[string]*StreamStub),
	}
}

func (this *Node) InitStubManager() {
	this.StubManager = NewStubManager(this, this.ID)
}

func (sm *StubManager) CloseStub(stubId string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	if stub, exists := sm.Stubs[stubId]; exists {
		stub.doClose()
	}
}

func (sm *StubManager) CloseStubByAddr(addr string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	code := "_http_frp_" + addr
	if stub, exists := sm.Stubs[code]; exists {
		stub.doClose()
	}
}

func (sm *StubManager) CloseAllStubs() {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	for _, stub := range sm.Stubs {
		stub.doClose()
	}
}

func (sm *StubManager) GetStubById(stubId string) (*StreamStub, bool) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	stub, exists := sm.Stubs[stubId]
	return stub, exists
}

func (sm *StubManager) DeleteAllStubs() {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	for id, _ := range sm.Stubs {
		delete(sm.Stubs, id)
	}
}

func (sm *StubManager) DeleteStubById(stubId string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	if _, exists := sm.Stubs[stubId]; exists {
		delete(sm.Stubs, stubId)
	}
}

func (sm *StubManager) RunFRPWithStub(code string) error {
	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	conn, err := net.Dial("tcp", config.LocalAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local target %s: %v", config.LocalAddr, err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))
	stub := NewStreamStub(sm.Node, conn, stubId, config.RemoteNode, "", sm.MyNodeId)

	sm.StubMutex.Lock()
	sm.Stubs[stubId] = stub
	sm.StubMutex.Unlock()

	params := map[string]interface{}{
		"code":    code,
		"stub_id": stubId,
	}
	result, ok := sm.Node.MakeRpcRequest(config.RemoteNode, "init", "_frp_connect", params)
	if !ok || result == nil {
		fmt.Println("conn close due error in rpc request")
		stub.doClose()
		return fmt.Errorf("RPC _frp_connect failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		fmt.Println("conn close due to error in rpc request return structure")
		stub.doClose()
		return fmt.Errorf("unexpected RPC response format")
	}

	peerStubVal, exists := resultMap["peer_stub_id"]
	if !exists {
		fmt.Println("conn close due to no peer stub id in rpc response")
		stub.doClose()
		return fmt.Errorf("RPC response missing 'peer_stub_id'")
	}
	peerStubId, ok := peerStubVal.(string)
	if !ok || peerStubId == "" {
		fmt.Println("conn close due to peer stub id in rpc response is wrongly set")
		stub.doClose()
		return fmt.Errorf("invalid 'peer_stub_id' in RPC response")
	}

	stub.peerStubId = peerStubId

	stub.StartSendLoop()
	stub.StartRecvLoop()

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

		node.StubManager.FrpConfigMutex.RLock()
		config, exists := node.StubManager.FrpConfigs[code]
		node.StubManager.FrpConfigMutex.RUnlock()
		if !exists {
			return map[string]interface{}{"error": fmt.Sprintf("FRP code '%s' not registered", code)}
		}
		fmt.Println("___________________----------------- The tcp server dial addr is " + config.RemoteAddr)
		conn, err := net.Dial("tcp", config.RemoteAddr)

		if err != nil {
			return map[string]interface{}{"error": fmt.Sprintf("failed to connect to RemoteAddr: %v", err)}
		}

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.StubManager.StubCounter, 1))
		stub := NewStreamStub(node, conn, localStubId, from, peerStubId, node.ID)

		node.StubManager.StubMutex.Lock()
		node.StubManager.Stubs[localStubId] = stub
		node.StubManager.StubMutex.Unlock()

		stub.StartSendLoop()
		stub.StartRecvLoop()

		return map[string]interface{}{
			"peer_stub_id": localStubId,
		}
	})

	node.registerRpcService("_frp_dynamic_connect", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		tcpTarget, ok := params["tcp_target"].(string)
		if !ok || tcpTarget == "" {
			return map[string]interface{}{"error": "missing or invalid 'tcp_target'"}
		}
		peerStubId, ok := params["stub_id"].(string)
		if !ok || peerStubId == "" {
			return map[string]interface{}{"error": "missing or invalid 'stub_id'"}
		}

		conn, err := net.Dial("tcp", tcpTarget)
		if err != nil {
			return map[string]interface{}{"error": fmt.Sprintf("failed to connect to tcp_target: %v", err)}
		}

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.StubManager.StubCounter, 1))
		stub := NewStreamStub(node, conn, localStubId, from, peerStubId, node.ID)

		node.StubManager.StubMutex.Lock()
		node.StubManager.Stubs[localStubId] = stub
		node.StubManager.StubMutex.Unlock()

		stub.StartSendLoop()
		stub.StartRecvLoop()

		return map[string]interface{}{
			"peer_stub_id": localStubId,
		}
	})

}

func (sm *StubManager) RunRegisteredFRP(code string) error {
	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("cannot find target frp")
	}
	localAddr := config.LocalAddr
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", localAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Println("FRP listener accept error:", err)
				continue
			}
			go func(clientConn net.Conn) {

				stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))
				sm.FrpConfigMutex.RLock()
				config, ok := sm.FrpConfigs[code]
				sm.FrpConfigMutex.RUnlock()
				if !ok {
					fmt.Printf("FRP code '%s' not registered\n", code)
					fmt.Println("conn close due to wrong rpc code")
					clientConn.Close()
					return
				}

				remoteNodeId := config.RemoteNode
				stub := NewStreamStub(sm.Node, clientConn, stubId, remoteNodeId, "", sm.MyNodeId)

				sm.StubMutex.Lock()
				sm.Stubs[stubId] = stub
				sm.StubMutex.Unlock()

				if err := sm.RunFRPWithExistingConn(code, stubId, clientConn); err != nil {
					fmt.Printf("FRP dynamic tunnel failed: %v\n", err)
					stub.doClose()
					return
				}

				stub.StartSendLoop()
				stub.StartRecvLoop()
				fmt.Println("-----------Reach the end of the connection establish------------")
			}(conn)
		}
	}()

	fmt.Println("Started FRP listener on", localAddr, "for code", code)
	return nil
}

func (sm *StubManager) RunFRPWithExistingConn(code, stubId string, conn net.Conn) error {
	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	params := map[string]interface{}{
		"code":    code,
		"stub_id": stubId,
	}
	result, ok := sm.Node.MakeRpcRequest(config.RemoteNode, "init", "_frp_connect", params)
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

	sm.StubMutex.Lock()
	if stub, ok := sm.Stubs[stubId]; ok {
		stub.peerStubId = peerStubId
	}
	sm.StubMutex.Unlock()

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

func (sm *StubManager) RegisteredTransferFileToRemote(code, localFilePath, remoteFilePath string) error {
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))

	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		file.Close()
		return fmt.Errorf("FRP code '%s' not registered", code)
	}
	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.Node, wrappedFile, stubId, config.RemoteNode, "", sm.MyNodeId)

	sm.StubMutex.Lock()
	sm.Stubs[stubId] = stub
	sm.StubMutex.Unlock()

	params := map[string]interface{}{
		"code":     code,
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "w",
	}
	result, ok := sm.Node.MakeRpcRequest(config.RemoteNode, "init", "_open_file", params)
	if !ok {
		return fmt.Errorf("RPC _open_file failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}
	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.StartSendLoop()

	return nil
}

func (node *Node) RegisterFileTransferRPCs() {
	node.registerRpcService("_open_file", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		code, _ := params["code"].(string)
		stubId, _ := params["stub_id"].(string)
		filename, _ := params["filename"].(string)
		mode, _ := params["mode"].(string)

		node.StubManager.FrpConfigMutex.RLock()
		_, exists := node.StubManager.FrpConfigs[code]
		node.StubManager.FrpConfigMutex.RUnlock()
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

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.StubManager.StubCounter, 1))
		wrappedFile := &fileConnAdapter{File: file}
		stub := NewStreamStub(node, wrappedFile, localStubId, from, stubId, node.ID)

		node.StubManager.StubMutex.Lock()
		node.StubManager.Stubs[localStubId] = stub
		node.StubManager.StubMutex.Unlock()

		if mode == "r" {
			stub.StartSendLoop()
		} else {
			stub.StartRecvLoop()
		}

		return map[string]interface{}{"ok": true, "peer_stub_id": localStubId}
	})

	node.registerRpcService("_close_file", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		stubId, _ := params["stub_id"].(string)
		if stubId == "" {
			return map[string]interface{}{"error": "missing stub_id"}
		}
		node.StubManager.CloseStub(stubId)
		return map[string]interface{}{"ok": true}
	})

	node.registerRpcService("_open_file_dynamic", "init", RPC_FuncInfo{}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		stubId, _ := params["stub_id"].(string)
		filename, _ := params["filename"].(string)
		mode, _ := params["mode"].(string)

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

		localStubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&node.StubManager.StubCounter, 1))
		wrappedFile := &fileConnAdapter{File: file}
		stub := NewStreamStub(node, wrappedFile, localStubId, from, stubId, node.ID)

		node.StubManager.StubMutex.Lock()
		node.StubManager.Stubs[localStubId] = stub
		node.StubManager.StubMutex.Unlock()

		if mode == "r" {
			stub.StartSendLoop()
		} else {
			stub.StartRecvLoop()
		}

		return map[string]interface{}{"ok": true, "peer_stub_id": localStubId}
	})

}

func (sm *StubManager) RegisteredDownloadFileFromRemote(code, remoteFilePath, localFilePath string) error {
	file, err := os.Create(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))

	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		file.Close()
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.Node, wrappedFile, stubId, config.RemoteNode, "", sm.MyNodeId)

	sm.StubMutex.Lock()
	sm.Stubs[stubId] = stub
	sm.StubMutex.Unlock()

	params := map[string]interface{}{
		"code":     code,
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "r",
	}
	result, ok := sm.Node.MakeRpcRequest(config.RemoteNode, "init", "_open_file", params)
	if !ok {
		return fmt.Errorf("RPC _open_file failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}

	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.StartRecvLoop()

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

func (sm *StubManager) RunFRPDynamicWithRegistration(code string) error {
	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	localAddr := config.LocalAddr
	remoteAddr := config.RemoteAddr
	remoteNode := config.RemoteNode
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", localAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Println("Dynamic FRP listener accept error:", err)
				continue
			}

			go func(clientConn net.Conn) {

				stub := NewStreamStub(sm.Node, clientConn, code, remoteNode, "", sm.MyNodeId)

				sm.StubMutex.Lock()
				sm.Stubs[code] = stub
				sm.StubMutex.Unlock()

				params := map[string]interface{}{
					"tcp_target": remoteAddr,
					"stub_id":    code,
				}
				result, ok := sm.Node.MakeRpcRequest(remoteNode, "init", "_frp_dynamic_connect", params)
				if !ok || result == nil {
					fmt.Println("Dynamic FRP RPC failed:", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					fmt.Println("Invalid RPC response format")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					fmt.Println("RPC response missing 'peer_stub_id'")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					fmt.Println("Invalid 'peer_stub_id' in RPC response")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				fmt.Println("Dynamic FRP tunnel established successfully:", code)
			}(conn)
		}
	}()

	fmt.Println("Started dynamic FRP listener on", localAddr)
	return nil
}

func (sm *StubManager) RunFRPDynamicWithRegistrationLocalAddr(localAddr string) error {
	code := WrapFRPAddr(localAddr)
	sm.FrpConfigMutex.RLock()
	config, ok := sm.FrpConfigs[code]
	sm.FrpConfigMutex.RUnlock()
	if !ok {
		return fmt.Errorf("FRP code '%s' not registered", code)
	}

	remoteAddr := config.RemoteAddr
	remoteNode := config.RemoteNode
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", localAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Println("Dynamic FRP listener accept error:", err)
				continue
			}

			go func(clientConn net.Conn) {

				stub := NewStreamStub(sm.Node, clientConn, code, remoteNode, "", sm.MyNodeId)

				sm.StubMutex.Lock()
				sm.Stubs[code] = stub
				sm.StubMutex.Unlock()

				params := map[string]interface{}{
					"tcp_target": remoteAddr,
					"stub_id":    code,
				}
				result, ok := sm.Node.MakeRpcRequest(remoteNode, "init", "_frp_dynamic_connect", params)
				if !ok || result == nil {
					fmt.Println("Dynamic FRP RPC failed:", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					fmt.Println("Invalid RPC response format")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					fmt.Println("RPC response missing 'peer_stub_id'")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					fmt.Println("Invalid 'peer_stub_id' in RPC response")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				fmt.Println("Dynamic FRP tunnel established successfully:", code)
			}(conn)
		}
	}()

	fmt.Println("Started dynamic FRP listener on", localAddr)
	return nil
}

func (sm *StubManager) RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr string) error {
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", localAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Println("Dynamic FRP listener accept error:", err)
				continue
			}

			go func(clientConn net.Conn) {
				code := "_http_frp_" + localAddr

				stub := NewStreamStub(sm.Node, clientConn, code, remoteNode, "", sm.MyNodeId)

				sm.StubMutex.Lock()
				sm.Stubs[code] = stub
				sm.StubMutex.Unlock()

				params := map[string]interface{}{
					"tcp_target": remoteAddr,
					"stub_id":    code,
				}
				result, ok := sm.Node.MakeRpcRequest(remoteNode, "init", "_frp_dynamic_connect", params)
				if !ok || result == nil {
					fmt.Println("Dynamic FRP RPC failed:", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					fmt.Println("Invalid RPC response format")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					fmt.Println("RPC response missing 'peer_stub_id'")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					fmt.Println("Invalid 'peer_stub_id' in RPC response")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				fmt.Println("Dynamic FRP tunnel established successfully:", code)
			}(conn)
		}
	}()

	fmt.Println("Started dynamic FRP listener on", localAddr)
	return nil
}

func (sm *StubManager) TransferFileToRemoteWithoutRegistration(remoteNode, localFilePath, remoteFilePath string) error {
	file, err := os.Open(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))
	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.Node, wrappedFile, stubId, remoteNode, "", sm.MyNodeId)

	sm.StubMutex.Lock()
	sm.Stubs[stubId] = stub
	sm.StubMutex.Unlock()

	params := map[string]interface{}{
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "w",
	}
	result, ok := sm.Node.MakeRpcRequest(remoteNode, "init", "_open_file_dynamic", params)
	if !ok || result == nil {
		stub.doClose()
		return fmt.Errorf("RPC _open_file_dynamic failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		stub.doClose()
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}

	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.StartSendLoop()

	return nil
}

func (sm *StubManager) DownloadFileFromRemoteWithoutRegistration(remoteNode, remoteFilePath, localFilePath string) error {
	file, err := os.Create(localFilePath)
	if err != nil {
		return fmt.Errorf("failed to create local file: %v", err)
	}

	stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))
	wrappedFile := &fileConnAdapter{File: file}
	stub := NewStreamStub(sm.Node, wrappedFile, stubId, remoteNode, "", sm.MyNodeId)

	sm.StubMutex.Lock()
	sm.Stubs[stubId] = stub
	sm.StubMutex.Unlock()

	params := map[string]interface{}{
		"stub_id":  stubId,
		"filename": remoteFilePath,
		"mode":     "r",
	}
	result, ok := sm.Node.MakeRpcRequest(remoteNode, "init", "_open_file_dynamic", params)
	if !ok || result == nil {
		stub.doClose()
		return fmt.Errorf("RPC _open_file_dynamic failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok || resultMap["ok"] != true {
		stub.doClose()
		return fmt.Errorf("remote file open failed: %v", resultMap)
	}

	peerStubId, _ := resultMap["peer_stub_id"].(string)
	stub.peerStubId = peerStubId

	stub.StartRecvLoop()

	return nil
}

func (sm *StubManager) RegisterFRPAndRun(remoteNode string, localAddr, remoteAddr string) error {
	if remoteNode == "" || localAddr == "" || remoteAddr == "" {
		return fmt.Errorf("remoteNode, localAddr, and remoteAddr must be non-empty")
	}
	return sm.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
}
