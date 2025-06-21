package main

import (
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

func (node *Node) floodRpcServices() {
	node.rpcFuncsMutex.RLock()
	functions := make(map[string]string)
	for funcName, providers := range node.rpcFuncs {
		for provider := range providers {
			functions[funcName] = provider
		}
	}
	node.rpcFuncsMutex.RUnlock()

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

func (node *Node) isDuplicateRpc(from string, invokeId int64) bool {
	key := fmt.Sprintf("%s:%d", from, invokeId)

	node.receivedRpcMutex.Lock()
	defer node.receivedRpcMutex.Unlock()

	if _, exists := node.receivedRpcCache[key]; exists {
		return true
	}
	node.receivedRpcCache[key] = time.Now()
	return false
}

func (node *Node) startCleanRpcCacheLoop() {
	ticker := time.NewTicker(RpcCacheCleanTicket)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-time.Minute * 10)
			node.receivedRpcMutex.Lock()
			for k, v := range node.receivedRpcCache {
				if v.Before(cutoff) {
					delete(node.receivedRpcCache, k)
				}
			}
			node.receivedRpcMutex.Unlock()
		}
	}
}

func (this *Node) registerRpcService(funcName string, provider string, info RPC_FuncInfo, callback RPC_REQ_CALLBACK) {
	this.rpcFuncsMutex.Lock()
	defer this.rpcFuncsMutex.Unlock()
	if _, ok := this.rpcFuncs[funcName]; !ok {
		this.rpcFuncs[funcName] = map[string]RPC_Func{}
	}
	this.rpcFuncs[funcName][provider] = RPC_Func{Callback: callback, Info: info}

	go this.floodRpcServices()
}

func (this *Node) unregisterRpcService(funcName string, provider string) {
	this.rpcFuncsMutex.Lock()
	defer this.rpcFuncsMutex.Unlock()
	if _, ok := this.rpcFuncs[funcName]; !ok {
		return
	}
	delete(this.rpcFuncs[funcName], provider)
	if len(this.rpcFuncs[funcName]) == 0 {
		delete(this.rpcFuncs, funcName)
	}
	go this.floodRpcServices()
}

func (this *Node) findTargetLocalRpcService(funcName string, provider string) *RPC_Func {
	this.rpcFuncsMutex.RLock()
	defer this.rpcFuncsMutex.RUnlock()
	if _, ok := this.rpcFuncs[funcName]; !ok {
		return nil
	}
	if _, ok := this.rpcFuncs[funcName][provider]; !ok {
		return nil
	}
	tRpc := this.rpcFuncs[funcName][provider]
	return &tRpc
}

func (node *Node) makeRpcRequest(peer string, targetFuncProvider string, fun string, params map[string]interface{}) (interface{}, bool) {
	if peer == node.ID {
		return node.invokeLocalRpc(targetFuncProvider, fun, params)
	}

	node.activeStubsMutex.Lock()
	node.addInvokedId()
	cId := atomic.LoadInt64(&node.invokedId)

	cRequestData := RPC_Request{
		Fun:    fun,
		Params: params,
	}
	data, err := json.Marshal(cRequestData)
	if err != nil {
		node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, "error marshaling request data: "+err.Error())
		node.activeStubsMutex.Unlock()
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
		TTL:        100,
		InvokeId:   cId,
		LastSender: node.ID,
	}

	stub := &RPC_Stub{
		Result:    make(chan interface{}),
		Target:    peer,
		Params:    params,
		TargetFun: fun,
	}
	node.activeStubs[cId] = stub
	node.activeStubsMutex.Unlock()

	defer func() {
		node.activeStubsMutex.Lock()
		delete(node.activeStubs, cId)
		node.activeStubsMutex.Unlock()
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

	case <-time.After(DefaultRpcOvertime):
		node.DebugPrint("makeRpcRequest_"+fun+"_"+peer, "timeout")
		return nil, false
	}
}

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
		TTL:        100,
		InvokeId:   msg.InvokeId,
		LastSender: node.ID,
	}
	go node.sendTo(msg.From, respMsg)
}

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
		TTL:        100,
		InvokeId:   msg.InvokeId,
		LastSender: node.ID,
	}
	go node.sendTo(msg.From, respMsg)
}

func (node *Node) handleRpcResponse(msg *Message) {
	node.DebugPrint("handleRpcResponse", msg.Print())

	var resp RPC_Resp
	err := json.Unmarshal([]byte(msg.MessageData.Data), &resp)
	if err != nil {
		node.DebugPrint("handleRpcResponse", "Failed to parse response: "+err.Error())
		return
	}

	node.activeStubsMutex.RLock()
	stub, exists := node.activeStubs[msg.InvokeId]
	node.activeStubsMutex.RUnlock()

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

func (node *Node) handleRpcServiceFlood(msg *Message) {
	var info RpcServiceInfo
	err := json.Unmarshal([]byte(msg.MessageData.Data), &info)
	if err != nil {
		node.DebugPrint("handleRpcServiceFlood", "invalid payload: "+err.Error())
		return
	}

	go func() {
		node.totalRpcsMutex.Lock()
		defer node.totalRpcsMutex.Unlock()

		for funcName, nodeMap := range node.totalRpcs {
			delete(nodeMap, info.NodeID)
			if len(nodeMap) == 0 {
				delete(node.totalRpcs, funcName)
			}
		}

		for funcName, provider := range info.Functions {
			if _, ok := node.totalRpcs[funcName]; !ok {
				node.totalRpcs[funcName] = make(map[string]string)
			}
			node.totalRpcs[funcName][info.NodeID] = provider
		}
	}()

	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1

	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

func (node *Node) PrintTotalRpcs() {
	node.totalRpcsMutex.RLock()
	defer node.totalRpcsMutex.RUnlock()

	fmt.Printf("Node %s: known RPC function providers\n", node.ID)
	if len(node.totalRpcs) == 0 {
		fmt.Println("  No RPC info available.")
		return
	}

	for funcName, nodeMap := range node.totalRpcs {
		fmt.Printf("  Function '%s':\n", funcName)
		for nodeID, provider := range nodeMap {
			fmt.Printf("    Node %s (entity: %s)\n", nodeID, provider)
		}
	}
}

func (node *Node) startPeriodicRpcFlood() {
	go func() {
		ticker := time.NewTicker(RpcFloodTicket)
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
	node.registerRpcService("whoami", "init", RPC_FuncInfo{Note: "Returns node ID"}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
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
		provider, ok := params["rpcProvider"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing or invalid 'provider' parameter"}
		}

		node.rpcFuncsMutex.RLock()
		defer node.rpcFuncsMutex.RUnlock()

		funcs := []map[string]string{}
		for funName, provMap := range node.rpcFuncs {
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
