package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type MajulaPackage struct {
	Method   string                 `json:"method"`
	Topic    string                 `json:"topic,omitempty"`
	Fun      string                 `json:"fun,omitempty"`
	Args     map[string]interface{} `json:"args,omitempty"`
	InvokeId int64                  `json:"invokeid,omitempty"`
	Result   interface{}            `json:"result,omitempty"`
}

type WsClientWrapper struct {
	Conn      *websocket.Conn
	RpcResult map[int64]chan interface{}
	RpcFuncs  map[string]RpcCallback
	SubFuncs  map[string]SubCallback
	Lock      sync.RWMutex
}

type RpcCallback func(fun string, args map[string]interface{}) interface{}
type SubCallback func(topic string, args map[string]interface{})

type RpcMeta struct {
	Parameters []map[string]string `json:"parameters,omitempty"` // {name: "userId", type: "string", note: "用户ID"}
	Results    []map[string]string `json:"results,omitempty"`
	Note       string              `json:"note,omitempty"`
}

// MajulaClient 封装底层通信、RPC、订阅、FRP、Nginx、文件等功能
type MajulaClient struct {
	Addr      string
	Entity    string
	Connected bool
	Conn      *websocket.Conn
	Ctx       context.Context
	Cancel    context.CancelFunc
	InvokeId  int64
	SendQueue chan MajulaPackage
	Wrapper   *WsClientWrapper
	Lock      sync.RWMutex
}

// NewMajulaClient 创建一个新的MajulaClient实例，初始化WebSocket连接和相关资源。
// 参数：addr - 服务器地址，entity - 客户端唯一标识。
// 返回：*MajulaClient 实例。
func NewMajulaClient(addr, entity string) *MajulaClient {
	ctx, cancel := context.WithCancel(context.Background())
	client := &MajulaClient{
		Addr:      addr,
		Entity:    entity,
		Ctx:       ctx,
		Cancel:    cancel,
		SendQueue: make(chan MajulaPackage, 1024),
		Wrapper: &WsClientWrapper{
			RpcResult: make(map[int64]chan interface{}),
			RpcFuncs:  make(map[string]RpcCallback),
			SubFuncs:  make(map[string]SubCallback),
		},
	}

	ready := make(chan struct{})

	// 实现同步注册
	go client.mainLoop(ready)

	<-ready
	return client
}

// RegisterClientID 向服务器注册当前客户端ID，便于后续通信。
func (c *MajulaClient) RegisterClientID() {
	content := map[string]interface{}{
		"client_id": c.Entity,
	}
	c.Send(MajulaPackage{
		Method: "REGISTER_CLIENT",
		Args:   content,
	})
	fmt.Println("Register client!!:", c.Entity)
}

// MajulaClient的主循环，负责建立WebSocket连接、自动重连、启动读写循环。
// 参数：ready - 用于通知外部连接已建立的通道。
func (c *MajulaClient) mainLoop(ready chan struct{}) {
	url := c.formatWsUrl()
	failures := 0
	for {
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err == nil {
			c.Conn = conn
			c.Connected = true
			c.Wrapper.Conn = conn
			c.RegisterClientID()
			log.Println("Connected to:", url)
			go c.readLoop()
			go c.sendLoop()
			c.RestoreState()
			close(ready)
			<-c.Ctx.Done()
			conn.Close()
			return
		}
		failures++
		if failures%10 == 0 {
			log.Println("Dial failed:", failures, "times")
		}
		delay := time.Duration(min(3000, 100+failures*100)) * time.Millisecond
		select {
		case <-time.After(delay):
		case <-c.Ctx.Done():
			return
		}
	}
}

// 格式化WebSocket连接地址，将http/https转换为ws/wss，并拼接实体ID。
// 返回：格式化后的WebSocket地址字符串。
func (c *MajulaClient) formatWsUrl() string {
	url := c.Addr
	if strings.HasPrefix(url, "http://") {
		url = strings.Replace(url, "http://", "ws://", 1)
	} else if strings.HasPrefix(url, "https://") {
		url = strings.Replace(url, "https://", "wss://", 1)
	}
	return url + "/majula/ws/" + c.Entity
}

// 持续读取服务器消息，解析后分发给对应处理函数。
func (c *MajulaClient) readLoop() {
	for c.Connected {
		_, data, err := c.Conn.ReadMessage()
		if err != nil {
			c.Connected = false
			c.Cancel()
			break
		}
		var msg MajulaPackage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		c.handleMessage(msg)
	}
}

// 持续从发送队列取消息并通过WebSocket发送到服务器。
func (c *MajulaClient) sendLoop() {
	for c.Connected {
		select {
		case <-c.Ctx.Done():
			c.Connected = false
			return
		case msg := <-c.SendQueue:
			data, _ := json.Marshal(msg)
			if c.Conn.WriteMessage(websocket.TextMessage, data) != nil {
				c.Connected = false
				return
			}
		}
	}
}

// 处理收到的各种类型的消息，包括RPC调用、订阅消息、私有消息等。
// 参数：msg - 接收到的MajulaPackage消息。
func (c *MajulaClient) handleMessage(msg MajulaPackage) {
	switch {
	case msg.Method == "RPC_CALL_FROM_REMOTE":
		c.Wrapper.Lock.RLock()
		handler, ok := c.Wrapper.RpcFuncs[msg.Fun]
		c.Wrapper.Lock.RUnlock()
		if ok {
			go func() {
				res := handler(msg.Fun, msg.Args)
				c.Send(MajulaPackage{
					Method:   "RETURN_RESULT",
					Fun:      msg.Fun,
					InvokeId: msg.InvokeId,
					Result:   res})
			}()
		}

	case msg.Method == "RPC_RESULT" && msg.InvokeId != 0:
		c.Wrapper.Lock.RLock()
		ch, ok := c.Wrapper.RpcResult[msg.InvokeId]
		c.Wrapper.Lock.RUnlock()
		if ok {
			select {
			case ch <- msg.Result:
			default:
			}
			c.Wrapper.Lock.Lock()
			delete(c.Wrapper.RpcResult, msg.InvokeId)
			c.Wrapper.Lock.Unlock()
		}

	case msg.Method == "PRIVATE_MESSAGE":
		c.Wrapper.Lock.RLock()
		handler, ok := c.Wrapper.SubFuncs["__private__"]
		c.Wrapper.Lock.RUnlock()
		if ok {
			go func() {
				handler("__private__", msg.Args)
			}()
		} else {
			log.Printf("[P2P] Message from %v: %+v\n", msg.Args["source_node"], msg.Args["message"])
		}

	case msg.Method == "SUB_RESULT":
		c.Wrapper.Lock.RLock()
		cb, ok := c.Wrapper.SubFuncs[msg.Topic]
		c.Wrapper.Lock.RUnlock()
		if ok {
			cb(msg.Topic, msg.Args)
		}

	default:
	}
}

// RegisterRpc 注册一个RPC方法到本地，供远程调用，并向服务器同步注册。
// 参数：fun - 方法名，handler - 回调函数，meta - 方法元信息。
func (c *MajulaClient) RegisterRpc(fun string, handler RpcCallback, meta *RpcMeta) {
	c.Wrapper.Lock.Lock()
	c.Wrapper.RpcFuncs[fun] = handler
	c.Wrapper.Lock.Unlock()

	args := map[string]interface{}{}
	if meta != nil {
		args["parameters"] = meta.Parameters
		args["results"] = meta.Results
		args["note"] = meta.Note
	}
	c.Send(MajulaPackage{Method: "REGISTER_RPC", Fun: fun, Args: args})
}

// UnregisterRpc 注销本地和服务器上的RPC方法。
// 参数：fun - 方法名。
func (c *MajulaClient) UnregisterRpc(fun string) {
	c.Wrapper.Lock.Lock()
	delete(c.Wrapper.RpcFuncs, fun)
	c.Wrapper.Lock.Unlock()

	c.Send(MajulaPackage{Method: "UNREGISTER_RPC", Fun: fun})
}

// CallRpc 同步调用远程节点的RPC方法。
// 参数：fun - 方法名，targetNode - 目标节点，provider - 服务提供者，args - 参数，timeout - 超时时间。
// 返回：远程返回结果和是否成功。
func (c *MajulaClient) CallRpc(fun string, targetNode string, provider string, args map[string]interface{}, timeout time.Duration) (interface{}, bool) {
	invokeId := atomic.AddInt64(&c.InvokeId, 1)
	ch := make(chan interface{}, 1)

	c.Wrapper.Lock.Lock()
	c.Wrapper.RpcResult[invokeId] = ch
	c.Wrapper.Lock.Unlock()
	if args == nil {
		args = make(map[string]interface{})
	}
	args["_target_node"] = targetNode
	args["_provider"] = provider

	c.Send(MajulaPackage{
		Method:   "RPC",
		Fun:      fun,
		Args:     args,
		InvokeId: invokeId,
	})

	select {
	case res := <-ch:
		return res, true
	case <-time.After(timeout):
		return nil, false
	}
}

// Subscribe 订阅指定主题，收到消息时回调处理。
// 参数：topic - 主题名，cb - 回调函数。
func (c *MajulaClient) Subscribe(topic string, cb SubCallback) {
	c.Wrapper.Lock.Lock()
	c.Wrapper.SubFuncs[topic] = cb
	c.Wrapper.Lock.Unlock()
	c.Send(MajulaPackage{Method: "SUBSCRIBE", Topic: topic})
}

// Unsubscribe 取消订阅指定主题。
// 参数：topic - 主题名。
func (c *MajulaClient) Unsubscribe(topic string) {
	c.Wrapper.Lock.Lock()
	delete(c.Wrapper.SubFuncs, topic)
	c.Wrapper.Lock.Unlock()

	c.Send(MajulaPackage{
		Method: "UNSUBSCRIBE",
		Topic:  topic,
	})
}

// Send 发送消息到发送队列，由sendLoop异步发送。
// 参数：msg - 要发送的MajulaPackage消息。
func (c *MajulaClient) Send(msg MajulaPackage) {
	defer func() { recover() }()
	c.SendQueue <- msg
}

// RestoreState 恢复客户端的订阅和RPC注册状态，通常用于重连后自动恢复。
func (c *MajulaClient) RestoreState() {
	c.Wrapper.Lock.RLock()
	defer c.Wrapper.Lock.RUnlock()

	for topic := range c.Wrapper.SubFuncs {
		c.Send(MajulaPackage{Method: "SUBSCRIBE", Topic: topic})
	}
	for fun := range c.Wrapper.RpcFuncs {
		c.Send(MajulaPackage{Method: "REGISTER_RPC", Fun: fun})
	}
}

// Publish 向指定主题发布消息。
// 参数：topic - 主题名，content - 消息内容。
func (c *MajulaClient) Publish(topic string, content map[string]interface{}) {
	c.Send(MajulaPackage{
		Method: "PUBLISH",
		Topic:  topic,
		Args:   content,
	})
}

// 启动心跳机制，定时向服务器发送心跳包保持连接。
// 参数：interval - 心跳间隔时间。
func (c *MajulaClient) startHeartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-c.Ctx.Done():
				return
			case <-ticker.C:
				c.Send(MajulaPackage{
					Method: "HEARTBEAT",
					Args: map[string]interface{}{
						"client_id":  c.Entity,
						"time_stamp": time.Now().Unix(),
					},
				})
			}
		}
	}()
}

// Quit 主动关闭客户端连接并通知服务器。
func (c *MajulaClient) Quit() {
	c.Send(MajulaPackage{
		Method: "QUIT",
		Args: map[string]interface{}{
			"client_id": c.Entity,
		},
	})
	c.Cancel()
}

// SendPrivateMessage 发送私有消息到指定节点和客户端。
// 参数：targetNode - 目标节点，targetClient - 目标客户端，payload - 消息内容。
func (c *MajulaClient) SendPrivateMessage(targetNode string, targetClient string, payload map[string]interface{}) {
	if payload == nil {
		payload = make(map[string]interface{})
	}

	data := map[string]interface{}{
		"payload": payload,
	}
	encoded, _ := json.Marshal(data)

	c.Send(MajulaPackage{
		Method: "SEND",
		Args: map[string]interface{}{
			"target_node":   targetNode,
			"content":       string(encoded),
			"target_client": targetClient,
		},
	})
}

// OnPrivateMessage 设置私有消息的回调处理函数。
// 参数：cb - 回调函数。
func (c *MajulaClient) OnPrivateMessage(cb SubCallback) {
	c.Wrapper.Lock.Lock()
	defer c.Wrapper.Lock.Unlock()
	c.Wrapper.SubFuncs["__private__"] = cb
}

// RegisterFRP 注册FRP端口转发，将本地端口映射到远程节点。
// 参数：code - 标识码，localAddr - 本地地址，remoteNode - 远程节点，remoteAddr - 远程地址。
func (c *MajulaClient) RegisterFRP(code, localAddr, remoteNode, remoteAddr string) {
	c.Send(MajulaPackage{
		Method: "REGISTER_FRP",
		Args: map[string]interface{}{
			"code":        code,
			"local_addr":  localAddr,
			"remote_node": remoteNode,
			"remote_addr": remoteAddr,
		},
	})
}

// CallRpcAsync 异步调用远程节点的RPC方法，结果通过回调返回。
// 参数同CallRpc，callback - 结果回调函数。
func (c *MajulaClient) CallRpcAsync(fun string, targetNode string, provider string, args map[string]interface{}, timeout time.Duration, callback func(result interface{}, ok bool)) {
	invokeId := atomic.AddInt64(&c.InvokeId, 1)
	ch := make(chan interface{}, 1)

	c.Wrapper.Lock.Lock()
	c.Wrapper.RpcResult[invokeId] = ch
	c.Wrapper.Lock.Unlock()

	if args == nil {
		args = make(map[string]interface{})
	}
	args["_target_node"] = targetNode
	args["_provider"] = provider

	c.Send(MajulaPackage{
		Method:   "RPC",
		Fun:      fun,
		Args:     args,
		InvokeId: invokeId,
	})

	go func() {
		select {
		case res := <-ch:
			callback(res, true)
		case <-time.After(timeout):
			callback(nil, false)
		}
		c.Wrapper.Lock.Lock()
		delete(c.Wrapper.RpcResult, invokeId)
		c.Wrapper.Lock.Unlock()
	}()
}

// RegisterFRPWithAddr 通过本地地址注册FRP端口转发，用本地地址注册。
// 参数：localAddr - 本地地址，remoteNode - 远程节点，remoteAddr - 远程地址。
func (c *MajulaClient) RegisterFRPWithAddr(localAddr, remoteNode, remoteAddr string) {
	c.Send(MajulaPackage{
		Method: "REGISTER_FRP_WITH_ADDR",
		Args: map[string]interface{}{
			"local_addr":  localAddr,
			"remote_node": remoteNode,
			"remote_addr": remoteAddr,
		},
	})
}

/*
func (c *MajulaClient) RegisterFRPTwoSide(code string, remoteNode string, targetAddr string, isServer bool) {
	args := map[string]interface{}{
		"code":        code,
		"remote_node": remoteNode,
		"target_addr": targetAddr,
		"is_server":   isServer,
	}
	c.Send(MajulaPackage{
		Method: "REGISTER_FRP_TWO_SIDE",
		Args:   args,
	})
}

*/

// StartFRPWithRegistration 启动已注册的FRP监听，使用指定code。
// 参数：code - 标识码。
func (c *MajulaClient) StartFRPWithRegistration(code string) {
	c.Send(MajulaPackage{
		Method: "START_FRP_LISTENER_WITH_REGISTRATION",
		Args: map[string]interface{}{
			"code": code,
		},
	})
}

// StartFRPWithoutRegistration 启动FRP监听（自动用地址注册），直接指定本地和远程信息。
// 参数：localAddr - 本地地址，remoteNode - 远程节点，remoteAddr - 远程地址。
func (c *MajulaClient) StartFRPWithoutRegistration(localAddr, remoteNode, remoteAddr string) {
	c.Send(MajulaPackage{
		Method: "START_FRP_LISTENER_WITHOUT_REGISTRATION",
		Args: map[string]interface{}{
			"local_addr":  localAddr,
			"remote_node": remoteNode,
			"remote_addr": remoteAddr,
		},
	})
}

// StartFRPWithLocalAddr 通过本地地址启动已注册的FRP监听。
// 参数：localAddr - 本地地址。
func (c *MajulaClient) StartFRPWithLocalAddr(localAddr string) {
	c.Send(MajulaPackage{
		Method: "START_FRP_LISTENER_WITH_LOCAL_ADDR",
		Args: map[string]interface{}{
			"local_addr": localAddr,
		},
	})
}

/*
func (c *MajulaClient) RegisterFRPAndRun(localAddr, remoteNode, remoteAddr string) {
	c.Send(MajulaPackage{
		Method: "REGISTER_FRP_AND_RUN",
		Args: map[string]interface{}{
			"local_addr":  localAddr,
			"remote_node": remoteNode,
			"remote_addr": remoteAddr,
		},
	})
}

*/

// RegisterNginxFRPAndRun 注册并运行Nginx FRP，将本地服务映射到远程。
// 参数：mappedAddr - 映射路径，remoteNode - 远程节点，hostAddr - 远程主机，extraArgs - 额外参数。
func (c *MajulaClient) RegisterNginxFRPAndRun(mappedAddr, remoteNode, hostAddr string, extraArgs map[string]string) {
	extraBytes, err := json.Marshal(extraArgs)
	if err != nil {
		log.Println("failed to marshal extraArgs", err)
		return
	}

	c.Send(MajulaPackage{
		Method: "REGISTER_NGINX_FRP_AND_RUN",
		Args: map[string]interface{}{
			"mapped_path": mappedAddr,
			"remote_node": remoteNode,
			"remote_url":  hostAddr,
			"extra_args":  string(extraBytes),
		},
	})
}

// RemoveNginxFRP 移除Nginx FRP映射。
// 参数同RegisterNginxFRPAndRun。
func (c *MajulaClient) RemoveNginxFRP(mappedAddr, remoteNode, hostAddr string, extraArgs map[string]string) {
	extraBytes, err := json.Marshal(extraArgs)
	if err != nil {
		log.Println("failed to marshal extraArgs", err)
		return
	}

	c.Send(MajulaPackage{
		Method: "UNREGISTER_NGINX_FRP",
		Args: map[string]interface{}{
			"mapped_path": mappedAddr,
			"remote_node": remoteNode,
			"remote_url":  hostAddr,
			"extra_args":  string(extraBytes),
		},
	})
}

// TransferFileToRemote 向远程节点传输本地文件。
// 参数：remoteNode - 远程节点，localPath - 本地文件路径，remotePath - 远程文件路径。
func (c *MajulaClient) TransferFileToRemote(remoteNode, localPath, remotePath string) {
	c.Send(MajulaPackage{
		Method: "UPLOAD_FILE",
		Args: map[string]interface{}{
			"remote_node": remoteNode,
			"local_path":  localPath,
			"remote_path": remotePath,
		},
	})
}

// DownloadFileFromRemote 从远程节点下载文件到本地。
// 参数：remoteNode - 远程节点，remotePath - 远程文件路径，localPath - 本地文件路径。
func (c *MajulaClient) DownloadFileFromRemote(remoteNode, remotePath, localPath string) {
	c.Send(MajulaPackage{
		Method: "DOWNLOAD_FILE",
		Args: map[string]interface{}{
			"remote_node": remoteNode,
			"remote_path": remotePath,
			"local_path":  localPath,
		},
	})
}

// 加入本地 learner
// 参数：group - raft group 名称，dbPath - learner 本地存储路径
func (c *MajulaClient) JoinRaftGroupAsLearner(group string, dbPath string) {
	pkg := MajulaPackage{
		Method: "JOIN_RAFT_LEARNER",
		Args: map[string]interface{}{
			"group":  group,
			"dbpath": dbPath,
		},
	}
	c.Send(pkg)
}

// 退出本地 learner
// 参数：group - raft group 名称
func (c *MajulaClient) LeaveRaftGroupAsLearner(group string) {
	pkg := MajulaPackage{
		Method: "LEAVE_RAFT_LEARNER",
		Args: map[string]interface{}{
			"group": group,
		},
	}
	c.Send(pkg)
}
