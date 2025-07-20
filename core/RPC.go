package core

import (
	"Majula/common"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type RpcServiceInfo struct {
	NodeID    string            `json:"node_id"`
	Functions map[string]string `json:"functions"` // map[funcName]provider
}

type RPC_REQ_CALLBACK func(fun string, params map[string]interface{}, from string, to string, invokeId int64) interface{}

type RPC_Stub struct {
	Result     chan interface{}
	Target     string
	Params     map[string]interface{}
	TargetFun  string
	ResendTime int64
}

type RPC_ParamInfo struct {
	Name string
	Type string
	Note string
}

type RPC_FuncInfo struct {
	Params  []RPC_ParamInfo
	Results []RPC_FuncInfo
	Note    string
}

type RPC_Func struct {
	Info     RPC_FuncInfo
	Callback RPC_REQ_CALLBACK
}

type RPC_Request struct {
	Fun    string
	Params map[string]interface{}
}

type RPC_Resp struct {
	Fun    string
	Result interface{}
	Error  string
}

// 广播本地注册的RPC服务信息到全网。
func (node *Node) floodRpcServices() {
	node.RpcFuncsMutex.RLock()
	functions := make(map[string]string)
	for funcName, providers := range node.RpcFuncs {
		for provider := range providers {
			functions[funcName] = provider
		}
	}
	node.RpcFuncsMutex.RUnlock()

	info := RpcServiceInfo{
		NodeID:    node.ID,
		Functions: functions,
	}
	data, _ := json.Marshal(info)

	msg := &Message{
		MessageData: MessageData{
			Type: RpcServiceFlood,
			Data: string(data),
		},
		From:       node.ID,
		TTL:        1,
		LastSender: node.ID,
		VersionSeq: uint64(atomic.LoadInt64(&node.MessageVersionCounter)),
	}
	node.addMessageCounter()

	for _, ch := range node.Channels {
		go ch.broadCast(msg)
	}
}

// 判断RPC请求是否重复。
// 参数：from - 来源节点，invokeId - 调用ID。
// 返回：是否重复。
func (node *Node) isDuplicateRpc(from string, invokeId int64) bool {
	key := fmt.Sprintf("%s:%d", from, invokeId)

	node.ReceivedRpcMutex.Lock()
	defer node.ReceivedRpcMutex.Unlock()

	if _, exists := node.ReceivedRpcCache[key]; exists {
		return true
	}
	node.ReceivedRpcCache[key] = time.Now()
	return false
}

// 启动定时清理RPC缓存的协程。
func (node *Node) startCleanRpcCacheLoop() {
	ticker := time.NewTicker(common.RpcCacheCleanTicket)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-time.Minute * 10)
			node.ReceivedRpcMutex.Lock()
			for k, v := range node.ReceivedRpcCache {
				if v.Before(cutoff) {
					delete(node.ReceivedRpcCache, k)
				}
			}
			node.ReceivedRpcMutex.Unlock()
		}
	}
}

// 注册本地RPC服务。
// 参数：funcName - 方法名，provider - 提供者，info - 方法信息，callback - 回调。
func (this *Node) registerRpcService(funcName string, provider string, info RPC_FuncInfo, callback RPC_REQ_CALLBACK) {
	this.RpcFuncsMutex.Lock()
	defer this.RpcFuncsMutex.Unlock()
	if _, ok := this.RpcFuncs[funcName]; !ok {
		this.RpcFuncs[funcName] = map[string]RPC_Func{}
	}
	this.RpcFuncs[funcName][provider] = RPC_Func{Callback: callback, Info: info}

	go this.floodRpcServices()
}

// 注销本地RPC服务。
// 参数：funcName - 方法名，provider - 提供者。
func (this *Node) unregisterRpcService(funcName string, provider string) {
	this.RpcFuncsMutex.Lock()
	defer this.RpcFuncsMutex.Unlock()
	if _, ok := this.RpcFuncs[funcName]; !ok {
		return
	}
	delete(this.RpcFuncs[funcName], provider)
	if len(this.RpcFuncs[funcName]) == 0 {
		delete(this.RpcFuncs, funcName)
	}
	go this.floodRpcServices()
}

// 查找本地RPC服务。
// 参数：funcName - 方法名，provider - 提供者。
// 返回：*RPC_Func。
func (this *Node) findTargetLocalRpcService(funcName string, provider string) *RPC_Func {
	this.RpcFuncsMutex.RLock()
	defer this.RpcFuncsMutex.RUnlock()
	if _, ok := this.RpcFuncs[funcName]; !ok {
		return nil
	}
	if _, ok := this.RpcFuncs[funcName][provider]; !ok {
		return nil
	}
	tRpc := this.RpcFuncs[funcName][provider]
	return &tRpc
}

// MakeRpcRequest 向远程节点发起RPC请求。
// 参数：peer - 目标节点，targetFuncProvider - 提供者，fun - 方法名，params - 参数。
// 返回：结果和是否成功。
func (node *Node) MakeRpcRequest(peer string, targetFuncProvider string, fun string, params map[string]interface{}) (interface{}, bool) {
	if peer == node.ID {
		return node.invokeLocalRpc(targetFuncProvider, fun, params)
	}

	node.ActiveStubsMutex.Lock()
	node.addInvokedId()
	cId := atomic.LoadInt64(&node.InvokedId)

	cRequestData := RPC_Request{
		Fun:    fun,
		Params: params,
	}
	data, err := json.Marshal(cRequestData)
	if err != nil {
		node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, "error marshaling request data: "+err.Error())
		node.ActiveStubsMutex.Unlock()
		return nil, false
	}

	msg := Message{
		MessageData: MessageData{
			Type:   RpcRequest,
			Data:   string(data),
			Entity: targetFuncProvider,
		},
		From:       node.ID,
		To:         peer,
		TTL:        common.DefaultMessageTTL,
		InvokeId:   cId,
		LastSender: node.ID,
	}

	stub := &RPC_Stub{
		Result:    make(chan interface{}),
		Target:    peer,
		Params:    params,
		TargetFun: fun,
	}
	node.ActiveStubs[cId] = stub
	node.ActiveStubsMutex.Unlock()

	defer func() {
		node.ActiveStubsMutex.Lock()
		delete(node.ActiveStubs, cId)
		node.ActiveStubsMutex.Unlock()
	}()

	defer func() {
		defer func() { recover() }()
		close(stub.Result)
	}()

	go node.sendTo(peer, &msg)

	select {
	case result, ok := <-stub.Result:
		if !ok {
			node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, "result channel closed")
			return nil, false
		}

		if resultMap, ok := result.(map[string]interface{}); ok {
			if errStr, exists := resultMap["error"]; exists {
				node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, fmt.Sprintf("RPC error: %v", errStr))
				return nil, false
			}
		}

		return result, true

	case <-time.After(common.DefaultRpcOvertime):
		node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, "timeout")
		return nil, false
	}
}

// 本地直接调用RPC。
// 参数：provider - 提供者，fun - 方法名，params - 参数。
// 返回：结果和是否成功。
func (node *Node) invokeLocalRpc(provider string, fun string, params map[string]interface{}) (interface{}, bool) {
	node.DebugPrint("invokeLocalRpc", fmt.Sprintf("invoking local RPC: fun=%s, provider=%s", fun, provider))

	targetFunc := node.findTargetLocalRpcService(fun, provider)
	if targetFunc == nil {
		node.DebugPrint("invokeLocalRpc", fmt.Sprintf("function '%s' with provider '%s' not found", fun, provider))
		return map[string]interface{}{"error": "function not found"}, false
	}
	if targetFunc.Callback == nil {
		node.DebugPrint("invokeLocalRpc", fmt.Sprintf("function '%s' has no callback", fun))
		return map[string]interface{}{"error": "callback not defined"}, false
	}

	var result interface{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				node.DebugPrint("invokeLocalRpc", fmt.Sprintf("panic occurred during callback execution: %v", r))
				result = map[string]interface{}{"error": "internal error"}
			}
		}()

		node.DebugPrint("invokeLocalRpc", fmt.Sprintf("calling callback: fun=%s, provider=%s", fun, provider))
		result = targetFunc.Callback(fun, params, node.ID, node.ID, 0)
		node.DebugPrint("invokeLocalRpc", fmt.Sprintf("callback completed: result=%+v", result))
	}()

	return result, true
}

// 处理收到的RPC请求消息。
// 参数：msg - 消息。
func (node *Node) handleRpcRequest(msg *Message) {
	node.DebugPrint("handleRpcRequest", msg.Print())

	var rpcReq RPC_Request
	err := json.Unmarshal([]byte(msg.MessageData.Data), &rpcReq)
	if err != nil {
		node.DebugPrint("handleRpcRequest", "Failed to parse request: "+err.Error())
		node.sendRpcErrorResponse(msg, "", "invalid JSON format")
		return
	}

	if node.isDuplicateRpc(msg.From, msg.InvokeId) {
		node.DebugPrint("handleRpcRequest", fmt.Sprintf("Duplicate invokeId %d from %s", msg.InvokeId, msg.From))
		node.sendRpcErrorResponse(msg, rpcReq.Fun, fmt.Sprintf("duplicate invokeId: %d", msg.InvokeId))
		return
	}

	targetFunc := node.findTargetLocalRpcService(rpcReq.Fun, msg.Entity)
	if targetFunc == nil || targetFunc.Callback == nil {
		node.DebugPrint("handleRpcRequest", "No RPC handler found for "+rpcReq.Fun+" by "+msg.Entity)
		node.sendRpcErrorResponse(msg, rpcReq.Fun, "function not found or no callback registered")
		return
	}

	var result interface{}

	func() {
		defer func() {
			if r := recover(); r != nil {
				node.DebugPrint("handleRpcRequest", fmt.Sprintf("Recovered from panic: %v", r))
				result = nil
			}
		}()
		result = targetFunc.Callback(rpcReq.Fun, rpcReq.Params, msg.From, node.ID, msg.InvokeId)
	}()

	resp := RPC_Resp{
		Fun:    rpcReq.Fun,
		Result: result,
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		node.DebugPrint("handleRpcRequest", "Failed to marshal response: "+err.Error())
		node.sendRpcErrorResponse(msg, rpcReq.Fun, "internal error: response marshal failed")
		return
	}

	respMsg := &Message{
		MessageData: MessageData{
			Type: RpcResponse,
			Data: string(respBytes),
		},
		From:       node.ID,
		To:         msg.From,
		TTL:        common.DefaultMessageTTL,
		InvokeId:   msg.InvokeId,
		LastSender: node.ID,
	}
	go node.sendTo(msg.From, respMsg)
}

// 发送RPC错误响应。
// 参数：msg - 原始消息，fun - 方法名，errMsg - 错误信息。
func (node *Node) sendRpcErrorResponse(msg *Message, fun string, errMsg string) {
	resp := RPC_Resp{
		Fun:   fun,
		Error: errMsg,
	}
	respBytes, err := json.Marshal(resp)
	if err != nil {
		node.DebugPrint("sendRpcErrorResponse", "Failed to marshal error response: "+err.Error())
		return
	}

	respMsg := &Message{
		MessageData: MessageData{
			Type: RpcResponse,
			Data: string(respBytes),
		},
		From:       node.ID,
		To:         msg.From,
		TTL:        common.DefaultMessageTTL,
		InvokeId:   msg.InvokeId,
		LastSender: node.ID,
	}
	go node.sendTo(msg.From, respMsg)
}

// 处理收到的RPC响应消息。
// 参数：msg - 消息。
func (node *Node) handleRpcResponse(msg *Message) {
	node.DebugPrint("handleRpcResponse", msg.Print())

	var resp RPC_Resp
	err := json.Unmarshal([]byte(msg.MessageData.Data), &resp)
	if err != nil {
		node.DebugPrint("handleRpcResponse", "Failed to parse response: "+err.Error())
		return
	}

	node.ActiveStubsMutex.RLock()
	stub, exists := node.ActiveStubs[msg.InvokeId]
	node.ActiveStubsMutex.RUnlock()

	if !exists {
		node.DebugPrint("handleRpcResponse", fmt.Sprintf("No stub found for invokeId: %d", msg.InvokeId))
		return
	}

	go func() {
		defer func() {
			recover()
		}()
		if resp.Error != "" {
			stub.Result <- map[string]interface{}{
				"error":  resp.Error,
				"fun":    resp.Fun,
				"target": stub.Target,
			}
		} else {
			stub.Result <- resp.Result
		}
	}()
}

// 处理RPC服务信息Flood消息。
// 参数：msg - 消息。
func (node *Node) handleRpcServiceFlood(msg *Message) {
	var info RpcServiceInfo
	err := json.Unmarshal([]byte(msg.MessageData.Data), &info)
	if err != nil {
		node.DebugPrint("handleRpcServiceFlood", "invalid payload: "+err.Error())
		return
	}

	go func() {
		node.TotalRpcsMutex.Lock()
		defer node.TotalRpcsMutex.Unlock()

		for funcName, nodeMap := range node.TotalRpcs {
			delete(nodeMap, info.NodeID)
			if len(nodeMap) == 0 {
				delete(node.TotalRpcs, funcName)
			}
		}

		for funcName, provider := range info.Functions {
			if _, ok := node.TotalRpcs[funcName]; !ok {
				node.TotalRpcs[funcName] = make(map[string]string)
			}
			node.TotalRpcs[funcName][info.NodeID] = provider
		}
	}()

	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1

	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

// PrintTotalRpcs 打印全网RPC服务信息。
func (node *Node) PrintTotalRpcs() {
	node.TotalRpcsMutex.RLock()
	defer node.TotalRpcsMutex.RUnlock()

	fmt.Printf("Node %s: known RPC function providers\n", node.ID)
	if len(node.TotalRpcs) == 0 {
		fmt.Println("  No RPC info available.")
		return
	}

	for funcName, nodeMap := range node.TotalRpcs {
		fmt.Printf("  Function '%s':\n", funcName)
		for nodeID, provider := range nodeMap {
			fmt.Printf("    Node %s (entity: %s)\n", nodeID, provider)
		}
	}
}

// 启动周期性RPC服务Flood广播。
func (node *Node) startPeriodicRpcFlood() {
	go func() {
		ticker := time.NewTicker(common.RpcFloodTicket)
		defer ticker.Stop()

		for {
			select {
			case <-node.HBctx.Done():
				node.DebugPrint("startPeriodicRpcFlood", "stopped due to HBctx cancel")
				return
			case <-ticker.C:
				node.DebugPrint("startPeriodicRpcFlood", "broadcasting RPC services")
				node.floodRpcServices()
			}
		}
	}()
}

// RegisterDefaultRPCs 注册默认RPC服务。
func (node *Node) RegisterDefaultRPCs() {
	// 加法
	node.registerRpcService("add", "init", RPC_FuncInfo{Note: "Adds two numbers"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		a, _ := params["a"].(float64)
		b, _ := params["b"].(float64)
		return map[string]interface{}{"sum": a + b}
	})

	// 回显
	node.registerRpcService("echo", "init", RPC_FuncInfo{Note: "Echo back parameters"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		return map[string]interface{}{"echo": params}
	})

	// 当前时间
	node.registerRpcService("time", "init", RPC_FuncInfo{Note: "Returns current server time"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		return map[string]interface{}{"time": time.Now().Format(time.RFC3339)}
	})

	// Whoami
	node.registerRpcService("whoami", "init", RPC_FuncInfo{Note: "Returns Node ID"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		return map[string]interface{}{"id": node.ID}
	})

	// 长文本测试
	node.registerRpcService("longtext", "init", RPC_FuncInfo{Note: "Returns long test string"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		return map[string]interface{}{"data": strings.Repeat("HelloWorld ", 100)}
	})

	// 随机整数
	node.registerRpcService("randint", "init", RPC_FuncInfo{Note: "Returns a random int between min and max"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		min, _ := params["min"].(float64)
		max, _ := params["max"].(float64)
		if max < min {
			min, max = max, min
		}
		n := int(min) + int(time.Now().UnixNano()%int64(max-min+1))
		return map[string]interface{}{"value": n}
	})

	// 反转字符串
	node.registerRpcService("reverse", "init", RPC_FuncInfo{Note: "Reverses a string"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		s, _ := params["s"].(string)
		runes := []rune(s)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return map[string]interface{}{"reversed": string(runes)}
	})

	// 列出当前注册的 RPC 在provider下的函数
	node.registerRpcService("allrpcs", "init", RPC_FuncInfo{Note: "List RPCs by provider"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		provider, ok := params["rpc_provider"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing or invalid 'provider' parameter"}
		}

		node.RpcFuncsMutex.RLock()
		defer node.RpcFuncsMutex.RUnlock()

		funcs := []map[string]string{}
		for funName, provMap := range node.RpcFuncs {
			if fn, exists := provMap[provider]; exists {
				funcs = append(funcs, map[string]string{
					"name": funName,
					"note": fn.Info.Note,
				})
			}
		}
		return funcs
	})

}
