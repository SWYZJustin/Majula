package core

import (
	"Majula/common"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type RpcServiceInfo struct {
	NodeID    string            `json:"node_id"`
	Functions map[string]string `json:"functions"` // 函数名到提供者的映射
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
	data, _ := common.MarshalAny(info)

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
	data, err := common.MarshalAny(cRequestData)
	if err != nil {
		Debug("RPC请求序列化失败", "函数=", fun, "目标=", peer, "错误=", err.Error())
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
			Debug("RPC结果通道已关闭", "函数=", fun, "目标=", peer)
			return nil, false
		}

		if resultMap, ok := result.(map[string]interface{}); ok {
			if errStr, exists := resultMap["error"]; exists {
				Debug("RPC调用错误", "函数=", fun, "目标=", peer, "错误=", errStr)
				return nil, false
			}
		}

		return result, true

	case <-time.After(common.DefaultRpcOvertime):
		Debug("RPC调用超时", "函数=", fun, "目标=", peer)
		return nil, false
	}
}

// 本地直接调用RPC。
// 参数：provider - 提供者，fun - 方法名，params - 参数。
// 返回：结果和是否成功。
func (node *Node) invokeLocalRpc(provider string, fun string, params map[string]interface{}) (interface{}, bool) {
	Debug("调用本地RPC", "函数=", fun, "提供者=", provider)

	targetFunc := node.findTargetLocalRpcService(fun, provider)
	if targetFunc == nil {
		Debug("本地RPC函数未找到", "函数=", fun, "提供者=", provider)
		return map[string]interface{}{"error": "function not found"}, false
	}
	if targetFunc.Callback == nil {
		Debug("本地RPC函数无回调", "函数=", fun)
		return map[string]interface{}{"error": "callback not defined"}, false
	}

	var result interface{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				Error("本地RPC回调执行时发生panic", "函数=", fun, "错误=", r)
				result = map[string]interface{}{"error": "internal error"}
			}
		}()

		Debug("调用RPC回调", "函数=", fun, "提供者=", provider)
		result = targetFunc.Callback(fun, params, node.ID, node.ID, 0)
		Debug("RPC回调执行完成", "函数=", fun, "结果=", result)
	}()

	return result, true
}

// 处理收到的RPC请求消息。
// 参数：msg - 消息。
func (node *Node) handleRpcRequest(msg *Message) {
	Debug("处理RPC请求", "消息=", msg.Print())

	var rpcReq RPC_Request
	err := common.UnmarshalAny([]byte(msg.MessageData.Data), &rpcReq)
	if err != nil {
		Error("解析RPC请求失败", "错误=", err.Error())
		node.sendRpcErrorResponse(msg, "", "invalid JSON format")
		return
	}

	if node.isDuplicateRpc(msg.From, msg.InvokeId) {
		Debug("重复的RPC调用ID", "调用ID=", msg.InvokeId, "来源=", msg.From)
		node.sendRpcErrorResponse(msg, rpcReq.Fun, fmt.Sprintf("duplicate invokeId: %d", msg.InvokeId))
		return
	}

	targetFunc := node.findTargetLocalRpcService(rpcReq.Fun, msg.Entity)
	if targetFunc == nil || targetFunc.Callback == nil {
		Debug("未找到RPC处理器", "函数=", rpcReq.Fun, "实体=", msg.Entity)
		node.sendRpcErrorResponse(msg, rpcReq.Fun, "function not found or no callback registered")
		return
	}

	var result interface{}

	func() {
		defer func() {
			if r := recover(); r != nil {
				Error("RPC请求处理时发生panic", "错误=", r)
				result = nil
			}
		}()
		result = targetFunc.Callback(rpcReq.Fun, rpcReq.Params, msg.From, node.ID, msg.InvokeId)
	}()

	resp := RPC_Resp{
		Fun:    rpcReq.Fun,
		Result: result,
	}
	respBytes, err := common.MarshalAny(resp)
	if err != nil {
		Error("序列化RPC响应失败", "错误=", err.Error())
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
	respBytes, err := common.MarshalAny(resp)
	if err != nil {
		Error("序列化RPC错误响应失败", "错误=", err.Error())
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
	Debug("处理RPC响应", "消息=", msg.Print())

	var resp RPC_Resp
	err := common.UnmarshalAny([]byte(msg.MessageData.Data), &resp)
	if err != nil {
		Error("解析RPC响应失败", "错误=", err.Error())
		return
	}

	node.ActiveStubsMutex.RLock()
	stub, exists := node.ActiveStubs[msg.InvokeId]
	node.ActiveStubsMutex.RUnlock()

	if !exists {
		Debug("未找到RPC存根", "调用ID=", msg.InvokeId)
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
	err := common.UnmarshalAny([]byte(msg.MessageData.Data), &info)
	if err != nil {
		Error("RPC服务广播负载无效", "错误=", err.Error())
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

	Log("节点", node.ID, "已知的RPC函数提供者")
	if len(node.TotalRpcs) == 0 {
		Log("  无RPC信息可用")
		return
	}

	for funcName, nodeMap := range node.TotalRpcs {
		Log("  函数", funcName, ":")
		for nodeID, provider := range nodeMap {
			Log("    节点", nodeID, "(实体:", provider, ")")
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
				Debug("周期性RPC广播已停止", "原因=心跳上下文取消")
				return
			case <-ticker.C:
				Debug("广播RPC服务")
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

	// 返回节点ID
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

	node.registerRpcService("addLearner", "raft", RPC_FuncInfo{
		Note: "Add a learner to a Raft group",
	}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		group, ok := params["group"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing group"}
		}
		learnerID, ok := params["learner_id"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing learner_id"}
		}

		node.RaftManager.RaftStubsMutex.RLock()
		raftClient, exists := node.RaftManager.RaftStubs[group]
		node.RaftManager.RaftStubsMutex.RUnlock()
		if !exists {
			return map[string]interface{}{"error": fmt.Sprintf("raft group %s not found", group)}
		}

		if raftClient.Role != Leader {
			raftClient.Mutex.Lock()
			res := map[string]interface{}{
				"success":     false,
				"redirect_to": raftClient.LeaderHint,
				"message":     "not leader",
			}
			raftClient.Mutex.Unlock()

			return res
		}

		raftClient.AddLearner(learnerID)

		return map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("learner %s added to group %s", learnerID, group),
		}
	})

	node.registerRpcService("removeLearner", "raft", RPC_FuncInfo{
		Note: "Remove a learner from a Raft group",
	}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		group, ok := params["group"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing group"}
		}
		learnerID, ok := params["learner_id"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing learner_id"}
		}

		node.RaftManager.RaftStubsMutex.RLock()
		raftClient, exists := node.RaftManager.RaftStubs[group]
		node.RaftManager.RaftStubsMutex.RUnlock()
		if !exists {
			return map[string]interface{}{"error": fmt.Sprintf("raft group %s not found", group)}
		}

		if raftClient.Role != Leader {
			raftClient.Mutex.Lock()
			res := map[string]interface{}{
				"success":     false,
				"redirect_to": raftClient.LeaderHint,
				"message":     "not leader",
			}
			raftClient.Mutex.Unlock()

			return res
		}

		raftClient.RemoveLearner(learnerID)

		return map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("learner %s removed from group %s", learnerID, group),
		}
	})

	// Raft Put操作
	node.registerRpcService("put", "raft", RPC_FuncInfo{
		Note: "Put a key-value pair to Raft group",
	}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		group, ok := params["group"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing group"}
		}
		key, ok := params["key"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing key"}
		}
		value, ok := params["value"]
		if !ok {
			return map[string]interface{}{"error": "missing value"}
		}

		// 检查本地是否有对应的Raft核心节点
		node.RaftManager.RaftStubsMutex.RLock()
		raftClient, exists := node.RaftManager.RaftStubs[group]
		node.RaftManager.RaftStubsMutex.RUnlock()

		if exists {
			// 本地有核心节点，直接处理
			cmd := RaftCommand{
				Op:    put,
				Key:   key,
				Value: value,
			}
			raftClient.HandleClientRequest(cmd)
			return map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("put command sent to local raft group %s", group),
			}
		} else {
			// 本地没有核心节点，需要转发给其他核心节点
			node.RaftManager.RaftPeersMutex.RLock()
			peers, hasPeers := node.RaftManager.RaftPeers[group]
			node.RaftManager.RaftPeersMutex.RUnlock()

			if !hasPeers {
				return map[string]interface{}{"error": fmt.Sprintf("no peers found for raft group %s", group)}
			}

			// 尝试转发给第一个可用的核心节点
			for _, peer := range peers {
				if peer == node.ID {
					continue // 跳过自己
				}
				result, ok := node.MakeRpcRequest(peer, "raft", "put", params)
				if ok {
					return result
				}
			}

			return map[string]interface{}{"error": fmt.Sprintf("failed to forward put command to any peer in group %s", group)}
		}
	})

	// Raft Delete操作
	node.registerRpcService("delete", "raft", RPC_FuncInfo{
		Note: "Delete a key from Raft group",
	}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		group, ok := params["group"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing group"}
		}
		key, ok := params["key"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing key"}
		}

		// 检查本地是否有对应的Raft核心节点
		node.RaftManager.RaftStubsMutex.RLock()
		raftClient, exists := node.RaftManager.RaftStubs[group]
		node.RaftManager.RaftStubsMutex.RUnlock()

		if exists {
			// 本地有核心节点，直接处理
			cmd := RaftCommand{
				Op:  delOp,
				Key: key,
			}
			raftClient.HandleClientRequest(cmd)
			return map[string]interface{}{
				"success": true,
				"message": fmt.Sprintf("delete command sent to local raft group %s", group),
			}
		} else {
			// 本地没有核心节点，需要转发给其他核心节点
			node.RaftManager.RaftPeersMutex.RLock()
			peers, hasPeers := node.RaftManager.RaftPeers[group]
			node.RaftManager.RaftPeersMutex.RUnlock()

			if !hasPeers {
				return map[string]interface{}{"error": fmt.Sprintf("no peers found for raft group %s", group)}
			}

			// 尝试转发给第一个可用的核心节点
			for _, peer := range peers {
				if peer == node.ID {
					continue // 跳过自己
				}
				result, ok := node.MakeRpcRequest(peer, "raft", "delete", params)
				if ok {
					return result
				}
			}

			return map[string]interface{}{"error": fmt.Sprintf("failed to forward delete command to any peer in group %s", group)}
		}
	})

	// Raft Get操作（只读操作，不需要通过Raft共识）
	node.registerRpcService("get", "raft", RPC_FuncInfo{
		Note: "Get a value from Raft group (read-only operation)",
	}, func(fun string, params map[string]interface{}, from, to string, invokeId int64) interface{} {
		group, ok := params["group"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing group"}
		}
		key, ok := params["key"].(string)
		if !ok {
			return map[string]interface{}{"error": "missing key"}
		}

		// 检查本地是否有对应的Raft核心节点
		node.RaftManager.RaftStubsMutex.RLock()
		raftClient, exists := node.RaftManager.RaftStubs[group]
		node.RaftManager.RaftStubsMutex.RUnlock()

		if exists {
			// 本地有核心节点，直接读取
			value, err := raftClient.Storage.GetState(group, key)
			if err != nil {
				return map[string]interface{}{"error": fmt.Sprintf("failed to get value: %v", err)}
			}
			return map[string]interface{}{
				"success": true,
				"value":   value,
			}
		} else {
			// 本地没有核心节点，需要转发给其他核心节点
			node.RaftManager.RaftPeersMutex.RLock()
			peers, hasPeers := node.RaftManager.RaftPeers[group]
			node.RaftManager.RaftPeersMutex.RUnlock()

			if !hasPeers {
				return map[string]interface{}{"error": fmt.Sprintf("no peers found for raft group %s", group)}
			}

			// 尝试转发给第一个可用的核心节点
			for _, peer := range peers {
				if peer == node.ID {
					continue // 跳过自己
				}
				result, ok := node.MakeRpcRequest(peer, "raft", "get", params)
				if ok {
					return result
				}
			}

			return map[string]interface{}{"error": fmt.Sprintf("failed to forward get command to any peer in group %s", group)}
		}
	})

}
