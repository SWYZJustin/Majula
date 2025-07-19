package api

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

	go client.mainLoop()
	return client
}

func (c *MajulaClient) RegisterClientID() {
	content := map[string]interface{}{
		"client_id": c.Entity,
	}
	c.Send(MajulaPackage{
		Method: "REGISTER_CLIENT",
		Args:   content,
	})
}

func (c *MajulaClient) mainLoop() {
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

func (c *MajulaClient) formatWsUrl() string {
	url := c.Addr
	if strings.HasPrefix(url, "http://") {
		url = strings.Replace(url, "http://", "ws://", 1)
	} else if strings.HasPrefix(url, "https://") {
		url = strings.Replace(url, "https://", "wss://", 1)
	}
	return url + "/ws/" + c.Entity
}

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

func (c *MajulaClient) handleMessage(msg MajulaPackage) {
	switch {
	case msg.Method == "RPC_CALL_FROM_REMOTE":
		c.Wrapper.Lock.RLock()
		handler, ok := c.Wrapper.RpcFuncs[msg.Fun]
		c.Wrapper.Lock.RUnlock()
		if ok {
			go func() {
				res := handler(msg.Fun, msg.Args)
				c.Send(MajulaPackage{Fun: msg.Fun, InvokeId: msg.InvokeId, Result: res})
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

// 注册一个rpc
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

func (c *MajulaClient) UnregisterRpc(fun string) {
	c.Wrapper.Lock.Lock()
	delete(c.Wrapper.RpcFuncs, fun)
	c.Wrapper.Lock.Unlock()

	c.Send(MajulaPackage{Method: "UNREGISTER_RPC", Fun: fun})
}

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

func (c *MajulaClient) Subscribe(topic string, cb SubCallback) {
	c.Wrapper.Lock.Lock()
	c.Wrapper.SubFuncs[topic] = cb
	c.Wrapper.Lock.Unlock()
	c.Send(MajulaPackage{Method: "SUBSCRIBE", Topic: topic})
}

func (c *MajulaClient) Unsubscribe(topic string) {
	c.Wrapper.Lock.Lock()
	delete(c.Wrapper.SubFuncs, topic)
	c.Wrapper.Lock.Unlock()

	c.Send(MajulaPackage{
		Method: "UNSUBSCRIBE",
		Topic:  topic,
	})
}

func (c *MajulaClient) Send(msg MajulaPackage) {
	defer func() { recover() }()
	c.SendQueue <- msg
}

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

func (c *MajulaClient) Publish(topic string, content map[string]interface{}) {
	c.Send(MajulaPackage{
		Method: "PUBLISH",
		Topic:  topic,
		Args:   content,
	})
}

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

func (c *MajulaClient) Quit() {
	c.Send(MajulaPackage{
		Method: "QUIT",
		Args: map[string]interface{}{
			"client_id": c.Entity,
		},
	})
	c.Cancel()
}

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

func (c *MajulaClient) OnPrivateMessage(cb SubCallback) {
	c.Wrapper.Lock.Lock()
	defer c.Wrapper.Lock.Unlock()
	c.Wrapper.SubFuncs["__private__"] = cb
}

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

func (c *MajulaClient) StartFRPWithRegistration(code string) {
	c.Send(MajulaPackage{
		Method: "START_FRP_LISTENER_WITH_REGISTRATION",
		Args: map[string]interface{}{
			"code": code,
		},
	})
}

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
