package main

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

type wsClientWrapper struct {
	conn      *websocket.Conn
	rpcResult map[int64]chan interface{}
	rpcFuncs  map[string]RpcCallback
	subFuncs  map[string]SubCallback
	lock      sync.RWMutex
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
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	invokeId  int64
	sendQueue chan MajulaPackage
	wrapper   *wsClientWrapper
	lock      sync.RWMutex
}

func NewMajulaClient(addr, entity string) *MajulaClient {
	ctx, cancel := context.WithCancel(context.Background())
	client := &MajulaClient{
		Addr:      addr,
		Entity:    entity,
		ctx:       ctx,
		cancel:    cancel,
		sendQueue: make(chan MajulaPackage, 1024),
		wrapper: &wsClientWrapper{
			rpcResult: make(map[int64]chan interface{}),
			rpcFuncs:  make(map[string]RpcCallback),
			subFuncs:  make(map[string]SubCallback),
		},
	}

	go client.mainLoop()
	return client
}

func (c *MajulaClient) registerClientID() {
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
			c.conn = conn
			c.Connected = true
			c.wrapper.conn = conn
			c.registerClientID()
			log.Println("Connected to:", url)
			go c.readLoop()
			go c.sendLoop()
			c.restoreState()
			<-c.ctx.Done()
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
		case <-c.ctx.Done():
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
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.Connected = false
			c.cancel()
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
		case <-c.ctx.Done():
			c.Connected = false
			return
		case msg := <-c.sendQueue:
			data, _ := json.Marshal(msg)
			if c.conn.WriteMessage(websocket.TextMessage, data) != nil {
				c.Connected = false
				return
			}
		}
	}
}

func (c *MajulaClient) handleMessage(msg MajulaPackage) {
	switch {
	case msg.Method == "RPC":
		c.wrapper.lock.RLock()
		handler, ok := c.wrapper.rpcFuncs[msg.Fun]
		c.wrapper.lock.RUnlock()
		if ok {
			res := handler(msg.Fun, msg.Args)
			c.Send(MajulaPackage{Fun: msg.Fun, InvokeId: msg.InvokeId, Result: res})
		}

	case msg.Method == "" && msg.InvokeId != 0:
		c.wrapper.lock.RLock()
		ch, ok := c.wrapper.rpcResult[msg.InvokeId]
		c.wrapper.lock.RUnlock()
		if ok {
			select {
			case ch <- msg.Result:
			default:
			}
			c.wrapper.lock.Lock()
			delete(c.wrapper.rpcResult, msg.InvokeId)
			c.wrapper.lock.Unlock()
		}

	case msg.Method == "PRIVATE_MESSAGE":
		c.wrapper.lock.RLock()
		handler, ok := c.wrapper.subFuncs["__private__"]
		c.wrapper.lock.RUnlock()
		if ok {
			handler("__private__", msg.Args)
		} else {
			log.Printf("[P2P] Message from %v: %+v\n", msg.Args["from_node"], msg.Args["message"])
		}

	default:
		c.wrapper.lock.RLock()
		cb, ok := c.wrapper.subFuncs[msg.Topic]
		c.wrapper.lock.RUnlock()
		if ok {
			cb(msg.Topic, msg.Args)
		}
	}
}

// 注册一个rpc
func (c *MajulaClient) RegisterRpc(fun string, handler RpcCallback, meta *RpcMeta) {
	c.wrapper.lock.Lock()
	c.wrapper.rpcFuncs[fun] = handler
	c.wrapper.lock.Unlock()

	args := map[string]interface{}{}
	if meta != nil {
		args["parameters"] = meta.Parameters
		args["results"] = meta.Results
		args["note"] = meta.Note
	}
	c.Send(MajulaPackage{Method: "REGISTER_RPC", Fun: fun, Args: args})
}

func (c *MajulaClient) CallRpc(fun string, targetNode string, provider string, args map[string]interface{}, timeout time.Duration) (interface{}, bool) {
	invokeId := atomic.AddInt64(&c.invokeId, 1)
	ch := make(chan interface{}, 1)

	c.wrapper.lock.Lock()
	c.wrapper.rpcResult[invokeId] = ch
	c.wrapper.lock.Unlock()
	if args == nil {
		args = make(map[string]interface{})
	}
	args["target_node"] = targetNode
	args["provider"] = provider

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
	c.wrapper.lock.Lock()
	c.wrapper.subFuncs[topic] = cb
	c.wrapper.lock.Unlock()
	c.Send(MajulaPackage{Method: "SUBSCRIBE", Topic: topic})
}

func (c *MajulaClient) Unsubscribe(topic string) {
	c.wrapper.lock.Lock()
	delete(c.wrapper.subFuncs, topic)
	c.wrapper.lock.Unlock()

	c.Send(MajulaPackage{
		Method: "UNSUBSCRIBE",
		Topic:  topic,
	})
}

func (c *MajulaClient) Send(msg MajulaPackage) {
	defer func() { recover() }()
	c.sendQueue <- msg
}

func (c *MajulaClient) restoreState() {
	c.wrapper.lock.RLock()
	defer c.wrapper.lock.RUnlock()

	for topic := range c.wrapper.subFuncs {
		c.Send(MajulaPackage{Method: "SUBSCRIBE", Topic: topic})
	}
	for fun := range c.wrapper.rpcFuncs {
		c.Send(MajulaPackage{Method: "REGISTER_RPC", Fun: fun})
	}
}

// 发布结构化事件
func (c *MajulaClient) PublishEvent(topic string, event map[string]interface{}) {
	c.Publish(topic, event)
}

// 发布二进制内容
func (c *MajulaClient) PublishRaw(topic string, base64Data string) {
	c.Publish(topic, map[string]interface{}{
		"base64_content_": base64Data,
	})
}

func (c *MajulaClient) Publish(topic string, content map[string]interface{}) {
	c.Send(MajulaPackage{
		Method: "PUBLISH",
		Topic:  topic,
		Args:   content,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *MajulaClient) startHeartbeat(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.Send(MajulaPackage{
					Method: "HEARTBEAT",
					Args: map[string]interface{}{
						"client_id": c.Entity,
						"timestamp": time.Now().Unix(),
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
	c.cancel()
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
	c.wrapper.lock.Lock()
	defer c.wrapper.lock.Unlock()
	c.wrapper.subFuncs["__private__"] = cb
}
