package core

import (
	"Majula/common"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// FRPConfig FRPConfig结构体，保存FRP端口转发的配置信息。
type FRPConfig struct {
	Code       string
	LocalAddr  string
	RemoteNode string
	RemoteAddr string
}

// StubManager StubManager结构体，管理所有StreamStub和FRP配置。
type StubManager struct {
	Node        *Node
	MyNodeId    string
	Stubs       map[string]*StreamStub
	StubMutex   sync.Mutex
	StubCounter int64

	FrpConfigs     map[string]*FRPConfig
	FrpConfigMutex sync.RWMutex
}

// RegisterFRP 注册一个FRP配置。
// 参数：config - FRPConfig配置。
// 返回：错误信息（如有）。
func (sm *StubManager) RegisterFRP(config *FRPConfig) error {
	sm.FrpConfigMutex.Lock()
	defer sm.FrpConfigMutex.Unlock()

	if sm.FrpConfigs == nil {
		sm.FrpConfigs = make(map[string]*FRPConfig)
	}
	sm.FrpConfigs[config.Code] = config

	return nil
}

// RegisterFRPWithCode 通过详细参数注册FRP配置。
// 参数：code/localAddr/remoteNode/remoteAddr - 配置信息。
// 返回：错误信息（如有）。
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

// RegisterFRPSimplified 通过简化参数注册FRP配置。
// 参数：code/remoteNode/targetAddr/isServer - 配置信息。
// 返回：错误信息（如有）。
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

// WrapFRPAddr 根据本地地址生成FRP code。
// 参数：localAddr - 本地地址。
// 返回：生成的code字符串。
func WrapFRPAddr(localAddr string) string {
	code := "_http_frp_" + localAddr
	return code
}

// RegisterFRPWithoutCode 注册无code的FRP配置，自动生成code。
// 参数：localAddr/remoteNode/remoteAddr - 配置信息。
// 返回：生成的code和错误信息。
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

// NewStubManager 创建一个新的StubManager实例。
// 参数：node - 所属节点，myNodeId - 节点ID。
// 返回：*StubManager 新建的管理器。
func NewStubManager(node *Node, myNodeId string) *StubManager {
	return &StubManager{
		Node:     node,
		MyNodeId: myNodeId,
		Stubs:    make(map[string]*StreamStub),
	}
}

// InitStubManager Node初始化StubManager。
func (this *Node) InitStubManager() {
	this.StubManager = NewStubManager(this, this.ID)
}

// CloseStub 关闭指定ID的Stub。
// 参数：stubId - Stub的ID。
func (sm *StubManager) CloseStub(stubId string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	if stub, exists := sm.Stubs[stubId]; exists {
		stub.doClose()
	}
}

// CloseStubByAddr 通过地址关闭Stub。
// 参数：addr - 地址。
func (sm *StubManager) CloseStubByAddr(addr string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	code := "_http_frp_" + addr
	if stub, exists := sm.Stubs[code]; exists {
		stub.doClose()
	}
}

// CloseAllStubs 关闭所有Stub。
func (sm *StubManager) CloseAllStubs() {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	for _, stub := range sm.Stubs {
		stub.doClose()
	}
}

// GetStubById 根据ID获取Stub。
// 参数：stubId - Stub的ID。
// 返回：*StreamStub和是否存在。
func (sm *StubManager) GetStubById(stubId string) (*StreamStub, bool) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	stub, exists := sm.Stubs[stubId]
	return stub, exists
}

// DeleteAllStubs 删除所有Stub。
func (sm *StubManager) DeleteAllStubs() {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()

	for id, _ := range sm.Stubs {
		delete(sm.Stubs, id)
	}
}

// DeleteStubById 删除指定ID的Stub。
// 参数：stubId - Stub的ID。
func (sm *StubManager) DeleteStubById(stubId string) {
	sm.StubMutex.Lock()
	defer sm.StubMutex.Unlock()
	if _, exists := sm.Stubs[stubId]; exists {
		delete(sm.Stubs, stubId)
	}
}

// RunFRPWithStub 通过Stub和FRP配置运行FRP连接。
// 参数：code - FRP code。
// 返回：错误信息（如有）。
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
		Error("RPC请求错误导致连接关闭")
		stub.doClose()
		return fmt.Errorf("RPC _frp_connect failed: %v", result)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		Error("RPC请求返回结构错误导致连接关闭")
		stub.doClose()
		return fmt.Errorf("unexpected RPC response format")
	}

	peerStubVal, exists := resultMap["peer_stub_id"]
	if !exists {
		Error("RPC响应中缺少对端存根ID导致连接关闭")
		stub.doClose()
		return fmt.Errorf("RPC response missing 'peer_stub_id'")
	}
	peerStubId, ok := peerStubVal.(string)
	if !ok || peerStubId == "" {
		Error("RPC响应中对端存根ID设置错误导致连接关闭")
		stub.doClose()
		return fmt.Errorf("invalid 'peer_stub_id' in RPC response")
	}

	stub.peerStubId = peerStubId

	stub.StartSendLoop()
	stub.StartRecvLoop()

	return nil
}

// RegisterFRPRPCHandler 注册FRP相关的RPC处理器。
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
		Debug("TCP服务器拨号地址", "地址=", config.RemoteAddr)
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

// RunRegisteredFRP 运行已注册的FRP配置，建立FRP连接。
// 参数：code - FRP code。
// 返回：错误信息（如有）。
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
				Error("FRP监听器接受连接错误", "错误=", err)
				continue
			}
			go func(clientConn net.Conn) {

				stubId := fmt.Sprintf("stub-%d", atomic.AddInt64(&sm.StubCounter, 1))
				sm.FrpConfigMutex.RLock()
				config, ok := sm.FrpConfigs[code]
				sm.FrpConfigMutex.RUnlock()
				if !ok {
					Error("FRP代码未注册", "代码=", code)
					Error("错误的RPC代码导致连接关闭")
					clientConn.Close()
					return
				}

				remoteNodeId := config.RemoteNode
				stub := NewStreamStub(sm.Node, clientConn, stubId, remoteNodeId, "", sm.MyNodeId)

				sm.StubMutex.Lock()
				sm.Stubs[stubId] = stub
				sm.StubMutex.Unlock()

				if err := sm.RunFRPWithExistingConn(code, stubId, clientConn); err != nil {
					Error("FRP动态隧道失败", "错误=", err)
					stub.doClose()
					return
				}

				stub.StartSendLoop()
				stub.StartRecvLoop()
				Debug("连接建立完成")
			}(conn)
		}
	}()

	Log("FRP监听器已启动", "本地地址=", localAddr, "代码=", code)
	return nil
}

// RunFRPWithExistingConn 使用已存在的连接运行FRP。
// 参数：code - FRP code，stubId - stub标识，conn - 已有连接。
// 返回：错误信息（如有）。
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
	Debug("RPC响应结果", "结果=", resultMap)

	peerStubVal, exists := resultMap["peer_stub_id"]
	Debug("获取到对端ID", "对端ID=", peerStubVal.(string))
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

// fileConnAdapter结构体，适配os.File为net.Conn。
type fileConnAdapter struct {
	*os.File
}

// SetReadDeadline 设置读取超时时间（无实际操作）。
func (f *fileConnAdapter) SetReadDeadline(t time.Time) error { return nil }

// SetWriteDeadline 设置写入超时时间（无实际操作）。
func (f *fileConnAdapter) SetWriteDeadline(t time.Time) error { return nil }

// SetDeadline 设置超时时间（无实际操作）。
func (f *fileConnAdapter) SetDeadline(t time.Time) error { return nil }

// LocalAddr 获取本地地址（伪造）。
func (f *fileConnAdapter) LocalAddr() net.Addr {
	return dummyAddr("file-local")
}

// RemoteAddr 获取远程地址（伪造）。
func (f *fileConnAdapter) RemoteAddr() net.Addr {
	return dummyAddr("file-remote")
}

// dummyAddr类型，实现net.Addr接口。
type dummyAddr string

// Network 获取网络类型。
func (d dummyAddr) Network() string { return "file" }

// 获取地址字符串。
func (d dummyAddr) String() string { return string(d) }

// RegisteredTransferFileToRemote 注册文件传输相关的RPC。
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

// RegisterFileTransferRPCs 注册文件传输相关的RPC处理器。
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

// RegisteredDownloadFileFromRemote 注册下载文件的RPC。
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

// 处理FRP相关的消息。
func (sm *StubManager) handleFrpMessages(msg *Message) {
	var stubId string

	switch msg.Type {
	case FRPData:
		var payload FRPDataPayload
		if err := common.UnmarshalAny([]byte(msg.Data), &payload); err != nil {
			Error("FRP数据反序列化错误", "错误=", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPAck:
		var payload FRPAckPayload
		if err := common.UnmarshalAny([]byte(msg.Data), &payload); err != nil {
			Error("FRP确认包反序列化错误", "错误=", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPResendRequest:
		var payload FRPResendRequestPayload
		if err := common.UnmarshalAny([]byte(msg.Data), &payload); err != nil {
			Error("FRP重传请求反序列化错误", "错误=", err)
			return
		}
		stubId = payload.TargetStubID

	case FRPClose:
		stubId = msg.Data

	default:
		Error("未处理的FRP消息类型", "类型=", msg.Type)
		return
	}

	stub, ok := sm.GetStubById(stubId)
	if !ok {
		Error("处理FRP消息时未找到存根", "存根ID=", stubId, "消息类型=", msg.Type)
		return
	}

	select {
	case stub.inChan <- frpMessage{msgType: msg.Type, payload: []byte(msg.Data)}:
	default:
		Error("处理FRP消息时存根输入通道已满，丢弃消息", "存根ID=", stubId)
	}
}

// RunFRPDynamicWithRegistration 动态注册并运行FRP，带注册。
// 参数：code - FRP code。
// 返回：错误信息（如有）。
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
				Error("动态FRP监听器接受连接错误", "错误=", err)
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
					Error("动态FRP RPC失败", "结果=", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					Error("RPC响应格式无效")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					Error("RPC响应缺少peer_stub_id")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					Error("RPC响应中的peer_stub_id无效")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				Log("动态FRP隧道建立成功", "代码=", code)
			}(conn)
		}
	}()

	Log("动态FRP监听器已启动", "本地地址=", localAddr)
	return nil
}

// RunFRPDynamicWithRegistrationLocalAddr 动态注册并运行FRP，使用本地地址。
// 参数：localAddr - 本地地址。
// 返回：错误信息（如有）。
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
				Error("动态FRP监听器接受连接错误", "错误=", err)
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
					Error("动态FRP RPC失败", "结果=", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					Error("RPC响应格式无效")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					Error("RPC响应缺少peer_stub_id")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					Error("RPC响应中的peer_stub_id无效")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				Log("动态FRP隧道建立成功", "代码=", code)
			}(conn)
		}
	}()

	Log("动态FRP监听器已启动", "本地地址=", localAddr)
	return nil
}

// RunFRPDynamicWithoutRegistration 动态运行FRP，无需注册。
// 参数：localAddr/remoteNode/remoteAddr - 配置信息。
// 返回：错误信息（如有）。
func (sm *StubManager) RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr string) error {
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", localAddr, err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				Error("动态FRP监听器接受连接错误", "错误=", err)
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
					Error("动态FRP RPC失败", "结果=", result)
					stub.doClose()
					return
				}

				resultMap, ok := result.(map[string]interface{})
				if !ok {
					Error("RPC响应格式无效")
					stub.doClose()
					return
				}

				peerStubVal, exists := resultMap["peer_stub_id"]
				if !exists {
					Error("RPC响应缺少peer_stub_id")
					stub.doClose()
					return
				}

				peerStubId, ok := peerStubVal.(string)
				if !ok || peerStubId == "" {
					Error("RPC响应中的peer_stub_id无效")
					stub.doClose()
					return
				}

				stub.peerStubId = peerStubId
				stub.StartSendLoop()
				stub.StartRecvLoop()

				Log("动态FRP隧道建立成功", "代码=", code)
			}(conn)
		}
	}()

	Log("动态FRP监听器已启动", "本地地址=", localAddr)
	return nil
}

// TransferFileToRemoteWithoutRegistration 无需注册直接向远程节点传输文件。
// 参数：remoteNode/localFilePath/remoteFilePath - 配置信息。
// 返回：错误信息（如有）。
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

// DownloadFileFromRemoteWithoutRegistration 无需注册直接从远程节点下载文件。
// 参数：remoteNode/remoteFilePath/localFilePath - 配置信息。
// 返回：错误信息（如有）。
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

// RegisterFRPAndRun 注册并运行FRP。
// 参数：remoteNode - 远程节点，localAddr/remoteAddr - 地址信息。
// 返回：错误信息（如有）。
func (sm *StubManager) RegisterFRPAndRun(remoteNode string, localAddr, remoteAddr string) error {
	if remoteNode == "" || localAddr == "" || remoteAddr == "" {
		return fmt.Errorf("remoteNode, localAddr, and remoteAddr must be non-empty")
	}
	return sm.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
}
