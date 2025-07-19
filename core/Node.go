package core

import (
	"Majula/api"
	"Majula/common"
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type MESSAGE_CALLBACK func(topic string, from string, to string, content []byte)

// 设定的常用类型昵称
type Node struct { // Node
	ID           string               // Node的id，每个node独有
	NodePeers    map[string]*NodePeer // map[直接连接的nodeId]nodePeer
	RoutingTable RoutingTableType     // 该node的路由表
	LinkSet      LinkSetType          // 已知的link集合
	Channels     map[string]*Channel  // node所有的channel
	HBctx        context.Context      // 用于退出的context
	HBcancel     context.CancelFunc   // 退出context的取消函数
	RetryQueue   []*Message           // 消息重发队列
	RetryMutex   sync.Mutex           // 用于消息重发的锁
	MsgMutex     sync.Mutex           // 用于消息发送的锁
	ReceivedMsgs map[string]bool      // 用于短暂存储收到消息的map，用于消息查重
	//LinkSetMutex          sync.Mutex           // 用于删改link集合的锁
	LinkSetMutex          sync.RWMutex
	MessageVersionCounter int64 // 发送消息的版本控制

	MyLinksVersion int64 // 自己的link的版本控制
	NodePeersMutex sync.RWMutex

	MySubs      map[string]map[string]MESSAGE_CALLBACK
	MySubsMutex sync.RWMutex

	TotalSubs      map[string][]string
	TotalSubsMutex sync.RWMutex

	InvokedId        int64
	ActiveStubs      map[int64]*RPC_Stub
	ActiveStubsMutex sync.RWMutex

	RpcFuncs      map[string]map[string]RPC_Func
	RpcFuncsMutex sync.RWMutex

	ReceivedRpcMutex sync.Mutex
	ReceivedRpcCache map[string]time.Time

	TotalRpcs      map[string]map[string]string
	TotalRpcsMutex sync.RWMutex

	ClientIDs      []string
	ClientIDsMutex sync.RWMutex

	WsServers      []*Server
	WsServersMutex sync.RWMutex

	StubManager *StubManager

	HttpProxyStubs      map[string]map[string]*HTTPProxyDefine
	HttpProxyStubsMutex sync.Mutex
}

type SubscriptionInfo struct {
	NodeID string   `json:"node_id"`
	Topics []string `json:"topics"`
}

func (node *Node) addLocalSub(pTopic string, pClientName string, cb MESSAGE_CALLBACK) {
	node.DebugPrint("addLocalSub", fmt.Sprintf("topic=%s client=%s", pTopic, pClientName))
	node.MySubsMutex.Lock()
	defer node.MySubsMutex.Unlock()
	if node.MySubs == nil {
		node.MySubs = make(map[string]map[string]MESSAGE_CALLBACK)
	}

	if _, ok := node.MySubs[pTopic]; ok {
		node.MySubs[pTopic][pClientName] = cb
		return
	}

	node.MySubs[pTopic] = make(map[string]MESSAGE_CALLBACK)
	node.MySubs[pTopic][pClientName] = cb

	msg := &Message{
		MessageData: MessageData{
			Type: TopicInit,
			Data: pTopic,
		},
		From:       node.ID,
		TTL:        1,
		LastSender: node.ID,
	}

	// 这个信息是需要version control的
	msg.VersionSeq = uint64(atomic.LoadInt64(&node.MessageVersionCounter))
	node.addMessageCounter()

	for _, channel := range node.Channels {
		go channel.broadCast(msg)
	}
}

func (node *Node) removeLocalSub(pTopic string, pClientName string) {
	node.DebugPrint("removeLocalSub", fmt.Sprintf("topic=%s client=%s", pTopic, pClientName))
	node.MySubsMutex.Lock()
	defer node.MySubsMutex.Unlock()

	subscribers, ok := node.MySubs[pTopic]
	if !ok {
		return
	}

	delete(subscribers, pClientName)
	if len(subscribers) == 0 {
		delete(node.MySubs, pTopic)
		msg := &Message{
			MessageData: MessageData{
				Type: TopicExit,
				Data: pTopic,
			},
			From:       node.ID,
			TTL:        1,
			LastSender: node.ID,
		}

		msg.VersionSeq = uint64(atomic.LoadInt64(&node.MessageVersionCounter))
		node.addMessageCounter()

		for _, channel := range node.Channels {
			go channel.broadCast(msg)
		}
	}
}

func (node *Node) clearSubClient(clientID string) {
	node.MySubsMutex.RLock()
	var topics []string
	for topic, subs := range node.MySubs {
		if _, ok := subs[clientID]; ok {
			topics = append(topics, topic)
		}
	}
	node.MySubsMutex.RUnlock()

	for _, topic := range topics {
		node.removeLocalSub(topic, clientID)
	}
}

func (node *Node) handleTopicInit(msg *Message) {
	topic := msg.Data
	sender := msg.From
	node.TotalSubsMutex.Lock()
	defer node.TotalSubsMutex.Unlock()
	node.DebugPrint("handleTopicInit", fmt.Sprintf("from=%s topic=%s", sender, topic))
	if node.TotalSubs == nil {
		node.TotalSubs = make(map[string][]string)
	}
	alreadyKnown := false
	if subs, ok := node.TotalSubs[topic]; ok {
		for _, sub := range subs {
			if sub == sender {
				alreadyKnown = true
				break
			}
		}
	}

	if !alreadyKnown {
		node.TotalSubs[topic] = append(node.TotalSubs[topic], sender)
	}
	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1

	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

func (node *Node) handleTopicExit(msg *Message) {
	topic := msg.Data
	sender := msg.From
	node.TotalSubsMutex.Lock()
	defer node.TotalSubsMutex.Unlock()

	if subs, ok := node.TotalSubs[topic]; ok {
		newSubs := []string{}
		for _, sub := range subs {
			if sub != sender {
				newSubs = append(newSubs, sub)
			}
		}
		if len(newSubs) == 0 {
			delete(node.TotalSubs, topic)
		} else {
			node.TotalSubs[topic] = newSubs
		}
	}

	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1
	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

func (node *Node) publishOnTopic(pTopic string, pMessage string) {
	node.DebugPrint("publishOnTopic", fmt.Sprintf("topic=%s msg=%s", pTopic, pMessage))
	node.MySubsMutex.RLock()
	if subs, ok := node.MySubs[pTopic]; ok {
		for _, cb := range subs {
			go cb(pTopic, node.ID, node.ID, []byte(pMessage))
		}
	}
	node.MySubsMutex.RUnlock()
	node.TotalSubsMutex.RLock()
	if node.TotalSubs == nil {
		return
	}
	targets, ok := node.TotalSubs[pTopic]
	node.TotalSubsMutex.RUnlock()
	if !ok || len(targets) == 0 {
		return
	}

	groupMap := map[string][]string{}

	for _, target := range targets {
		routes, ok := node.RoutingTable[target]
		if !ok || len(routes) == 0 {
			continue
		}
		nextHop := routes[0].nextHopNodeID
		groupMap[nextHop] = append(groupMap[nextHop], target)
	}

	for nextHop, toList := range groupMap {
		if len(toList) == 1 {
			msg := &Message{
				MessageData: MessageData{
					Type: TopicPublish,
					Data: fmt.Sprintf("%s|%s", pTopic, pMessage),
				},
				From:       node.ID,
				TTL:        common.DefaultMessageTTL,
				LastSender: node.ID,
				To:         toList[0],
			}
			node.sendTo(msg.To, msg)
		} else {
			msg := &Message{
				MessageData: MessageData{
					Type:     TopicPublish,
					Data:     fmt.Sprintf("%s|%s", pTopic, pMessage),
					BundleTo: toList,
				},
				From:       node.ID,
				TTL:        1,
				LastSender: node.ID,
			}
			node.sendTo(nextHop, msg)
		}
	}
}

type RoutingTableType map[string][]RouteEntry
type LinkSetType map[string]map[string]Link

type LinkWorkerType int // LinkWorker类型

const (
	VoidWorker LinkWorkerType = iota
	SimpleWorker
)

// 将link转成字符串传输
func (node *Node) serializeLinkSet() string {
	node.LinkSetMutex.RLock()
	defer node.LinkSetMutex.RUnlock()
	filteredLinkSet := make(LinkSetType)

	for key1, innerMap := range node.LinkSet {
		if _, ok := filteredLinkSet[key1]; !ok {
			filteredLinkSet[key1] = map[string]Link{}
		}
		for key2, link := range innerMap {
			if link.Cost != -1 {
				filteredLinkSet[key1][key2] = link
			}
		}
	}
	data, err := json.Marshal(filteredLinkSet)
	if err != nil {
		log.Printf("Failed to serialize LinkSet: %v", err)
		return ""
	}
	return string(data)
}

// link字符串解析
func deserializeLinkSet(linkSetStr string) LinkSetType {
	var linkSet LinkSetType
	err := json.Unmarshal([]byte(linkSetStr), &linkSet)
	if err != nil {
		log.Printf("Failed to deserialize LinkSet: %v", err)
		return nil
	}
	return linkSet
}

type RouteEntry struct { //路由表的一项
	LocalChannelID string
	nextHopNodeID  string
}

// 打印路由表
func (node *Node) printRoutingTable() {
	fmt.Printf("%s的路由表:\n", node.ID)
	if node.RoutingTable == nil || len(node.RoutingTable) == 0 {
		fmt.Println("路由表为空")
		return
	}

	fmt.Println("路由表内容:")
	for destination, routes := range node.RoutingTable {
		fmt.Printf("目标节点: %s\n", destination)
		for i, route := range routes {
			fmt.Printf("  路由 %d:\n", i+1)
			fmt.Printf("    本地通道 ID: %s\n", route.LocalChannelID)
			fmt.Printf("    下一跳节点 ID: %s\n", route.nextHopNodeID)
		}
	}
}

type NodeStatus int // Node的状态标记
const (
	Active NodeStatus = iota
	Inactive
)

func (n *Node) DebugPrint(name string, message string) {
	if !common.DebugPrint {
		return
	}
	fmt.Printf("[%s: %s] %s\n", n.ID, name, message)
}

type NodePeer struct {
	LastActiveTime time.Time
	Status         NodeStatus
}

// 新建一个Node，但是包括相关的参数
func NewNodeWithChannel(pID string, pChannels map[string]*Channel) *Node {
	HBctx, HBcancel := context.WithCancel(context.Background())
	aNode := Node{
		ID:                    pID,
		NodePeers:             make(map[string]*NodePeer),
		RoutingTable:          make(RoutingTableType),
		LinkSet:               make(LinkSetType),
		Channels:              pChannels,
		HBctx:                 HBctx,
		HBcancel:              HBcancel,
		RetryQueue:            make([]*Message, 0),
		RetryMutex:            sync.Mutex{},
		MsgMutex:              sync.Mutex{},
		ReceivedMsgs:          make(map[string]bool),
		LinkSetMutex:          sync.RWMutex{},
		MessageVersionCounter: 0,
		MyLinksVersion:        0,
		NodePeersMutex:        sync.RWMutex{},
		MySubs:                make(map[string]map[string]MESSAGE_CALLBACK),
		MySubsMutex:           sync.RWMutex{},
		TotalSubs:             make(map[string][]string),
		TotalSubsMutex:        sync.RWMutex{},
		InvokedId:             0,
		ActiveStubs:           make(map[int64]*RPC_Stub),
		ActiveStubsMutex:      sync.RWMutex{},
		RpcFuncsMutex:         sync.RWMutex{},
		RpcFuncs:              make(map[string]map[string]RPC_Func),
		ReceivedRpcCache:      make(map[string]time.Time),
		ReceivedRpcMutex:      sync.Mutex{},
		TotalRpcs:             make(map[string]map[string]string),
		TotalRpcsMutex:        sync.RWMutex{},

		ClientIDs:      make([]string, 0),
		ClientIDsMutex: sync.RWMutex{},

		WsServers:      make([]*Server, 0),
		WsServersMutex: sync.RWMutex{},

		HttpProxyStubs:      make(map[string]map[string]*HTTPProxyDefine),
		HttpProxyStubsMutex: sync.Mutex{},
	}
	aNode.InitStubManager()
	return &aNode
}

// 完全新建Node
func NewNode(pID string) *Node {
	HBctx, HBcancel := context.WithCancel(context.Background())
	aNode := Node{
		ID:                    pID,
		NodePeers:             make(map[string]*NodePeer),
		RoutingTable:          make(RoutingTableType),
		LinkSet:               make(LinkSetType),
		Channels:              make(map[string]*Channel),
		HBctx:                 HBctx,
		HBcancel:              HBcancel,
		RetryQueue:            make([]*Message, 0),
		RetryMutex:            sync.Mutex{},
		MsgMutex:              sync.Mutex{},
		ReceivedMsgs:          make(map[string]bool),
		LinkSetMutex:          sync.RWMutex{},
		MessageVersionCounter: 0,
		MyLinksVersion:        0,
		NodePeersMutex:        sync.RWMutex{},
		MySubs:                make(map[string]map[string]MESSAGE_CALLBACK),
		MySubsMutex:           sync.RWMutex{},
		TotalSubs:             make(map[string][]string),
		TotalSubsMutex:        sync.RWMutex{},
		InvokedId:             0,
		ActiveStubs:           make(map[int64]*RPC_Stub),
		ActiveStubsMutex:      sync.RWMutex{},
		RpcFuncsMutex:         sync.RWMutex{},
		RpcFuncs:              make(map[string]map[string]RPC_Func),
		ReceivedRpcCache:      make(map[string]time.Time),
		ReceivedRpcMutex:      sync.Mutex{},
		TotalRpcs:             make(map[string]map[string]string),
		TotalRpcsMutex:        sync.RWMutex{},

		ClientIDs:      make([]string, 0),
		ClientIDsMutex: sync.RWMutex{},

		WsServers:           make([]*Server, 0),
		WsServersMutex:      sync.RWMutex{},
		HttpProxyStubs:      make(map[string]map[string]*HTTPProxyDefine),
		HttpProxyStubsMutex: sync.Mutex{},
	}
	aNode.InitStubManager()
	return &aNode
}

func (this *Node) addGlobalLinkVersion() {
	atomic.AddInt64(&this.MyLinksVersion, 1)
}

func (this *Node) addInvokedId() {
	atomic.AddInt64(&this.InvokedId, 1)
}

func (this *Node) setGlobalLinkVersion(newVersion int64) {
	atomic.AddInt64(&this.MyLinksVersion, newVersion)
}

// 为一个Node添加一个Channel，channel的id是由node分配的
func (node *Node) AddChannel(pChannel *Channel) {
	channelID := fmt.Sprintf("%s-%d", node.ID, len(node.Channels)+1)
	pChannel.ID = channelID
	pChannel.HNode = node
	node.Channels[channelID] = pChannel
}

// 获取一个channel
func (node *Node) GetChannel(channelID string) *Channel {
	return node.Channels[channelID]
}

func (node *Node) PrintAllChannels() {
	node.DebugPrint("PrintAllChannels", "start")
	for _, channel := range node.Channels {
		node.DebugPrint("PrintAllChannels-each", channel.ID)
	}
}

// 增加消息累积数
func (node *Node) addMessageCounter() {
	atomic.AddInt64(&node.MessageVersionCounter, 1)
}

// 注册一个node，即让它开始运行
func (node *Node) Register() {
	node.DebugPrint("Register", "start")
	node.hello()
	node.CheckPeersNew()
	go node.buildUp()
	go node.collectAndCheckPeers()
	go node.startRetryLoop()
	go node.cleanupReceivedMsgs()
	go node.heartbeat()
	go node.startSubscriptionFloodLoop()
	go node.startCleanRpcCacheLoop()
	go node.startPeriodicRpcFlood()
	go node.RegisterDefaultRPCs()
	go node.RegisterFRPRPCHandler()
	go node.RegisterFileTransferRPCs()
}

// 使一个node退出
func (node *Node) Quit() {
	node.DebugPrint("Quit", "start")
	node.sendQuit()
	node.HBcancel()
	node.WsServersMutex.Lock()
	defer node.WsServersMutex.Unlock()
	for _, server := range node.WsServers {
		server.Shutdown()
	}
	node.WsServers = nil
}

// 将一条消息发送，可指定channelId，不然就是从路由表找
func (node *Node) sendTo(targetId string, msg *Message, channelID ...string) {
	//Node.DebugPrint(msg.Print(), DebugSend)
	msg.VersionSeq = uint64(atomic.LoadInt64(&node.MessageVersionCounter))
	node.addMessageCounter()
	msg.To = targetId
	routes, ok := node.RoutingTable[targetId]
	if !ok || len(routes) == 0 {
		msg.Lost = true
		go node.addToRetryQueue(msg)
		return
	}

	var localChannelID string
	if len(channelID) > 0 {
		localChannelID = channelID[0]
	} else {
		localChannelID = routes[0].LocalChannelID
	}

	nextHopID := routes[0].nextHopNodeID
	channel, ok := node.Channels[localChannelID]
	msg.Route = nextHopID
	if !ok {
		msg.Lost = true
		go node.addToRetryQueue(msg)
		return
	}
	node.DebugPrint("send", msg.Print())
	if msg.Type == Other {
		fmt.Println("send:", msg.Print())
	}
	go channel.send(nextHopID, msg)
}

// 将一条消息添加到消息重发队列
func (node *Node) addToRetryQueue(msg *Message) {
	node.RetryMutex.Lock()
	defer node.RetryMutex.Unlock()
	node.DebugPrint("addToRetryQueue", msg.Print())
	node.RetryQueue = append(node.RetryQueue, msg)
}

// 生成一个messageKey用于查重
func (node *Node) generateMsgKey(msg *Message) string {
	return fmt.Sprintf("%s-%d-%d", msg.From, msg.VersionSeq, msg.MessageData.Type)
}

// bundlesend是用来处理topic发送的
func (node *Node) handleBundle(originalMsg *Message, topic, payload string) {
	newBundle := map[string][]string{}
	node.MySubsMutex.RLock()
	for _, realTo := range originalMsg.MessageData.BundleTo {
		if realTo == node.ID {
			node.DebugPrint("topic-msg", fmt.Sprintf("[Topic %s] %s", topic, payload))
			if subs, ok := node.MySubs[topic]; ok {
				for _, cb := range subs {
					go cb(topic, originalMsg.From, node.ID, []byte(payload))
				}
			}
		} else {
			routes, ok := node.RoutingTable[realTo]
			if !ok || len(routes) == 0 {
				continue
			}
			nextHop := routes[0].nextHopNodeID
			newBundle[nextHop] = append(newBundle[nextHop], realTo)
		}
	}
	node.MySubsMutex.RUnlock()

	for nextHop, targets := range newBundle {
		copyMsg := *originalMsg
		copyMsg.To = ""
		copyMsg.Route = ""
		copyMsg.MessageData.BundleTo = targets
		node.sendTo(nextHop, &copyMsg)
	}
}

// 接受消息
func (node *Node) onRecv(peerId string, msg *Message) {
	node.DebugPrint("onRecv", msg.Print())
	if msg.Type == Other {
		fmt.Println("onRecv:", msg.Print())
	}
	msgKey := node.generateMsgKey(msg)
	node.MsgMutex.Lock()
	defer node.MsgMutex.Unlock()

	// 这里是不需要查重的信息的部分 stage1
	// 接受广播的hello消息
	if msg.Type == Hello {
		node.DebugPrint("onRecv-handleHello", "start")
		sender := msg.From
		node.NodePeersMutex.Lock()
		defer node.NodePeersMutex.Unlock()
		if _, ok := node.NodePeers[sender]; !ok {
			//println(Node.ID, "onRecv", msg.Type, sender)
			//fmt.Println(Node.ID + " add " + sender + " to Node peer")
			node.DebugPrint("onRecv-hello-addPeer", sender)
			node.NodePeers[sender] = &NodePeer{
				LastActiveTime: time.Now(),
				Status:         Active,
			}
		}
		return
	}

	if msg.Type == HeartBeat {
		node.DebugPrint("onRecv-heartBeat", "start")
		go node.handleHeartbeat(msg)
		return
	}

	// 信息查重
	if _, exists := node.ReceivedMsgs[msgKey]; exists {
		return
	}
	node.ReceivedMsgs[msgKey] = true

	// Stage2: 需要查重的广播信息

	switch msg.MessageData.Type {
	case TopicInit:
		go node.handleTopicInit(msg)
		return
	case TopicExit:
		go node.handleTopicExit(msg)
		return
	case TopicSubscribeFlood:
		go node.handleSubscribeFlood(msg)
		return
	case RpcServiceFlood:
		go node.handleRpcServiceFlood(msg)
		return
	default:
	}

	// 如果消息是发给本node的
	if msg.To == node.ID {
		if msg.isFrp() {
			go node.StubManager.handleFrpMessages(msg)
			return
		}

		switch msg.MessageData.Type {
		case Quit:
			node.DebugPrint("onRecv-Quit", "start")
			quitId := msg.From
			go func() {
				node.NodePeersMutex.Lock()
				defer node.NodePeersMutex.Unlock()
				delete(node.NodePeers, quitId)
			}()

		case TopicPublish:
			topic, payload, ok := parseTopicMessage(msg.MessageData.Data)
			if !ok {
				fmt.Println("Invalid topic message:", msg.MessageData.Data)
				break
			}

			if len(msg.MessageData.BundleTo) > 0 {
				go node.handleBundle(msg, topic, payload)
				break
			}

			// 非 bundle 情况
			node.DebugPrint("topic-msg", fmt.Sprintf("[Topic %s] %s", topic, payload))
			node.MySubsMutex.RLock()
			if subs, ok := node.MySubs[topic]; ok {
				for _, cb := range subs {
					go cb(topic, msg.From, node.ID, []byte(payload))
				}
			}
			node.MySubsMutex.RUnlock()

		case RpcRequest:
			node.DebugPrint("onRecv-rpcRequest", "start")
			go node.handleRpcRequest(msg)
			return

		case RpcResponse:
			node.DebugPrint("onRecv-rpcResponse", "start")
			go node.handleRpcResponse(msg)
			return

		case P2PMessage:
			var payload map[string]interface{}
			err := json.Unmarshal([]byte(msg.Data), &payload)
			if err != nil {
				node.DebugPrint("onRecv-p2pMessage", "parseError")
				return
			}

			targetClientID, ok := payload["target_client"].(string)
			if !ok {
				node.DebugPrint("onRecv-p2pMessage", "missing target_client")
				return
			}

			node.WsServersMutex.RLock()
			defer node.WsServersMutex.RUnlock()

			for _, wsServer := range node.WsServers {
				err := wsServer.SendToClient(targetClientID, api.MajulaPackage{
					Method: "PRIVATE_MESSAGE",
					Args: map[string]interface{}{
						"source_node": msg.From,
						"message":     payload["payload"],
					},
				})
				if err == nil {
					break
				}
			}

		default:
			//Node.DebugPrint("onRecv-Message", msg.Data)
			fmt.Println("Receive message: " + msg.Data)
		}
		// 确实是发给我的，但我只是转发用
	} else if msg.Route == node.ID {
		//fmt.Println("Trans: " + msg.Data)
		msg.TTL -= 1
		if msg.TTL <= 0 {
			return
		}

		targetID := msg.To
		routes, ok := node.RoutingTable[targetID]
		if !ok || len(routes) == 0 {
			msg.Lost = true
			go node.addToRetryQueue(msg)
			return
		}
		localChannelID := routes[0].LocalChannelID
		nextHopID := routes[0].nextHopNodeID
		channel, ok := node.Channels[localChannelID]
		msg.Route = nextHopID
		if !ok {
			msg.Lost = true
			go node.addToRetryQueue(msg)
			return
		}
		msg.LastSender = node.ID
		node.DebugPrint("onRecv-transmit", msg.Print())
		//fmt.Println("onRecv-TransFinal", msg.Print())
		go channel.send(nextHopID, msg)

		// 误发或者广播，但不是发给我，直接丢弃
	} else {
		node.DebugPrint("onRecv-dropMessage", msg.Print())
		return
	}
}

func (node *Node) handleHeartbeat(msg *Message) {
	from := msg.From
	now := time.Now()

	node.NodePeersMutex.Lock()
	peer, exists := node.NodePeers[from]
	if !exists {
		node.NodePeers[from] = &NodePeer{LastActiveTime: now, Status: Active}
	} else {
		peer.LastActiveTime = now
		peer.Status = Active
	}
	node.NodePeersMutex.Unlock()

	linkSetStr := msg.MessageData.Data
	receivedLinkSet := deserializeLinkSet(linkSetStr)
	if receivedLinkSet == nil {
		return
	}

	node.LinkSetMutex.Lock()
	for source, links := range receivedLinkSet {
		if _, exists := node.LinkSet[source]; !exists {
			node.LinkSet[source] = make(map[string]Link)
		}
		for target, link := range links {
			if existingLink, exists := node.LinkSet[source][target]; !exists || link.Version > existingLink.Version {
				link.setLastUpdateTime()
				node.LinkSet[source][target] = link
			} else {
				l := node.LinkSet[source][target]
				l.setLastUpdateTime()
				node.LinkSet[source][target] = l
			}
		}
	}
	node.LinkSetMutex.Unlock()
}

// 开始一个重复发送的循环
func (node *Node) startRetryLoop() {
	for {
		select {
		case <-node.HBctx.Done():
			return
		default:
			node.RetryMutex.Lock()
			if len(node.RetryQueue) > 0 {
				msg := node.RetryQueue[0]
				node.RetryQueue = node.RetryQueue[1:]
				node.RetryMutex.Unlock()
				node.sendTo(msg.To, msg)
			} else {
				node.RetryMutex.Unlock()
			}
			time.Sleep(common.RetryLoopPeriod)
		}
	}
}

// 定期清空消息记录
func (node *Node) cleanupReceivedMsgs() {
	for {
		select {
		case <-node.HBctx.Done():
			return
		default:
			node.MsgMutex.Lock()
			for key := range node.ReceivedMsgs {
				delete(node.ReceivedMsgs, key)
			}
			node.MsgMutex.Unlock()
			time.Sleep(common.ReceivedMessageCleanUpPeriod)
		}
	}
}

func (node *Node) hello() {
	msg := &Message{
		MessageData: MessageData{
			Type: Hello,
			Data: node.ID,
		},
		From:       node.ID,
		TTL:        1,
		Lost:       false,
		LastSender: node.ID,
	}
	msg.VersionSeq = uint64(atomic.LoadInt64(&node.MessageVersionCounter))
	node.addMessageCounter()
	for _, channel := range node.Channels {
		println(node.ID, channel.ID)
		channel.broadCast(msg)
	}
}

// 发送心跳，连带着linkset的信息
func (node *Node) heartbeat() {
	ticker := time.NewTicker(common.HeartBeatTimePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			linkSetStr := node.serializeLinkSet()

			msg := &Message{
				MessageData: MessageData{
					Type: HeartBeat,
					Data: linkSetStr,
				},
				From:       node.ID,
				To:         "",
				Route:      "",
				TTL:        1,
				Lost:       false,
				LastSender: node.ID,
			}
			for _, channel := range node.Channels {
				go channel.broadCast(msg)
			}
		}
	}
}

// 发送退出信息
func (node *Node) sendQuitRequest(peerId string) {
	msg := &Message{
		MessageData: MessageData{
			Type: Quit,
			Data: peerId,
		},
		From:       node.ID,
		To:         peerId,
		Route:      peerId,
		TTL:        1,
		Lost:       false,
		LastSender: node.ID,
	}
	node.sendTo(peerId, msg)
}

func (node *Node) sendQuit() {

	node.NodePeersMutex.RLock()
	defer node.NodePeersMutex.RUnlock()
	for peerId := range node.NodePeers {
		node.sendQuitRequest(peerId)
	}
}

// 更新link的代价，并且更新linkset
func (node *Node) updateLinkCost(peerId string, cost int64) error {
	node.LinkSetMutex.Lock()
	defer node.LinkSetMutex.Unlock()

	if _, exists := node.LinkSet[node.ID]; !exists {
		return fmt.Errorf("LinkSet for Node %s does not exist", node.ID)
	}
	if _, exists := node.LinkSet[node.ID][peerId]; !exists {
		return fmt.Errorf("Link from %s to %s does not exist", node.ID, peerId)
	}
	link := node.LinkSet[node.ID][peerId]
	link.setCost(cost)
	node.LinkSet[node.ID][peerId] = link
	return nil
}

// 构建路由表
func (node *Node) buildUp() {
	ticker := time.NewTicker(common.BuildUpTimePeriod)
	defer ticker.Stop()
	isCheckLinks := false
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			if isCheckLinks {
				go node.checkOutOfDateLinks()
			} else {
				go node.buildRoutingTable()
			}
			isCheckLinks = !isCheckLinks
		}
	}
}

func (node *Node) checkOutOfDateLinks() {
	keysToDelete := make(map[string]map[string]bool)

	node.LinkSetMutex.RLock()
	for key1, links := range node.LinkSet {
		for key2, link := range links {
			if link.checkOutOfTime() {
				if _, exists := keysToDelete[key1]; !exists {
					keysToDelete[key1] = make(map[string]bool)
				}
				keysToDelete[key1][key2] = true
			}
		}
	}
	node.LinkSetMutex.RUnlock()
	if len(keysToDelete) == 0 {
		return
	}
	node.LinkSetMutex.Lock()
	defer node.LinkSetMutex.Unlock()
	for key1, innerMap := range keysToDelete {
		for key2 := range innerMap {
			delete(node.LinkSet[key1], key2)
		}
		if len(node.LinkSet[key1]) == 0 {
			delete(node.LinkSet, key1)
		}
	}

}

// 用最短路径算法计算路由表
func (node *Node) buildRoutingTable() {
	node.LinkSetMutex.Lock()
	defer node.LinkSetMutex.Unlock()
	tempRoutingTable := make(RoutingTableType)
	dist := make(map[string]int64)
	prev := make(map[string]string)
	visited := make(map[string]bool)
	pq := make(PriorityQueue, 0)

	for _, targets := range node.LinkSet {
		for _, link := range targets {
			dist[link.Source] = math.MaxInt64
			dist[link.Target] = math.MaxInt64
		}
	}
	dist[node.ID] = 0

	heap.Push(&pq, &Item{
		NodeID: node.ID,
		Cost:   0,
	})

	for pq.Len() > 0 {
		currentItem := heap.Pop(&pq).(*Item)
		currentNode := currentItem.NodeID
		if visited[currentNode] {
			continue
		}
		visited[currentNode] = true
		for neighbor, link := range node.LinkSet[currentNode] {
			if !visited[neighbor] {
				newCost := dist[currentNode] + link.Cost
				if newCost < dist[neighbor] {
					dist[neighbor] = newCost
					prev[neighbor] = currentNode
					heap.Push(&pq, &Item{
						NodeID: neighbor,
						Cost:   newCost,
					})
				}
			}
		}
	}

	for target := range dist {
		if target == node.ID {
			continue
		}
		path := []string{target}
		for prev[path[0]] != node.ID {
			path = append([]string{prev[path[0]]}, path...)
		}
		nextHop := path[0]
		localChannelID := ""
		if link, exists := node.LinkSet[node.ID][nextHop]; exists {
			localChannelID = link.Channel
		}
		tempRoutingTable[target] = []RouteEntry{
			{
				LocalChannelID: localChannelID,
				nextHopNodeID:  nextHop,
			},
		}
	}

	if !node.compareRoutingTables(tempRoutingTable) {
		node.RoutingTable = tempRoutingTable
	}
}

// 比较新建路由表和旧路由表
func (node *Node) compareRoutingTables(tempTable RoutingTableType) bool {
	if len(node.RoutingTable) != len(tempTable) {
		return false
	}

	for target, routes := range node.RoutingTable {
		tempRoutes, exists := tempTable[target]
		if !exists || len(routes) != len(tempRoutes) {
			return false
		}
		for i := range routes {
			if routes[i] != tempRoutes[i] {
				return false
			}
		}
	}

	return true
}

func (node *Node) collectLinkPaths() {
	for channelID, channel := range node.Channels {
		for peerID := range channel.ChannelPeers {
			node.NodePeersMutex.Lock()
			if _, ok := node.NodePeers[peerID]; !ok {
				node.NodePeers[peerID] = &NodePeer{}
			}
			node.NodePeersMutex.Unlock()
			link := Link{
				Source:  node.ID,
				Target:  peerID,
				Cost:    -1,
				Version: 0,
				Channel: channelID,
			}
			node.LinkSetMutex.Lock()
			if _, exists := node.LinkSet[node.ID]; !exists {
				node.LinkSet[node.ID] = make(map[string]Link)
			}
			if _, ok := node.LinkSet[node.ID][peerID]; !ok {
				node.LinkSet[node.ID][peerID] = link
			}
			node.LinkSetMutex.Unlock()
		}
	}
}

func (node *Node) CheckPeersNew() {
	for _, channel := range node.Channels {
		go channel.checkCost()
	}
}

func (node *Node) collectAndCheckPeers() {
	ticker := time.NewTicker(common.CostCheckTimePeriod)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			node.addGlobalLinkVersion()
			//Node.collectLinkPaths()
			node.CheckPeersNew()
		}
	}
}

func (node *Node) linkUpdateFromChannel(link Link) {
	node.DebugPrint("linkupdate", link.printLinkS())
	node.LinkSetMutex.Lock()
	defer node.LinkSetMutex.Unlock()
	if _, ok := node.LinkSet[link.Source]; !ok {
		node.LinkSet[link.Source] = make(map[string]Link)
	}
	if currentLink, ok := node.LinkSet[link.Source][link.Target]; !ok || !costEqual(currentLink.Cost, link.Cost) {
		link.setLastUpdateTime()
		node.LinkSet[link.Source][link.Target] = link
	} else {
		l := node.LinkSet[link.Source][link.Target]
		if link.Version > l.Version {
			l.updateVersion(link.Version)
		}
		l.setLastUpdateTime()
		node.LinkSet[link.Source][link.Target] = l
	}
}

func (node *Node) floodAllSubscriptions() {
	node.MySubsMutex.RLock()
	topics := make([]string, 0, len(node.MySubs))
	for topic := range node.MySubs {
		topics = append(topics, topic)
	}
	node.MySubsMutex.RUnlock()

	if len(topics) == 0 {
		return
	}

	subInfo := SubscriptionInfo{
		NodeID: node.ID,
		Topics: topics,
	}
	dataBytes, _ := json.Marshal(subInfo)

	msg := &Message{
		MessageData: MessageData{
			Type: TopicSubscribeFlood,
			Data: string(dataBytes),
		},
		From:       node.ID,
		TTL:        1,
		LastSender: node.ID,
		VersionSeq: uint64(atomic.LoadInt64(&node.MessageVersionCounter)),
	}
	node.addMessageCounter()

	for _, channel := range node.Channels {
		go channel.broadCast(msg)
	}
}

func (node *Node) handleSubscribeFlood(msg *Message) {
	var info SubscriptionInfo
	err := json.Unmarshal([]byte(msg.MessageData.Data), &info)
	if err != nil {
		node.DebugPrint("handleSubscribeFlood", "invalid payload")
		return
	}

	node.TotalSubsMutex.Lock()
	for _, topic := range info.Topics {
		found := false
		for _, peer := range node.TotalSubs[topic] {
			if peer == info.NodeID {
				found = true
				break
			}
		}
		if !found {
			node.TotalSubs[topic] = append(node.TotalSubs[topic], info.NodeID)
		}
	}
	node.TotalSubsMutex.Unlock()

	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1
	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

// 周期性广播本地订阅的 Flood 逻辑
func (node *Node) startSubscriptionFloodLoop() {
	ticker := time.NewTicker(common.SubscribeFloodTicket)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			node.floodAllSubscriptions()
		}
	}
}

func (node *Node) PrintTotalSubs() {
	node.TotalSubsMutex.RLock()
	defer node.TotalSubsMutex.RUnlock()

	fmt.Printf("%s 的订阅总表（TotalSubs）如下：\n", node.ID)
	if len(node.TotalSubs) == 0 {
		fmt.Println("  无订阅记录")
		return
	}

	for topic, subs := range node.TotalSubs {
		fmt.Printf("  Topic '%s': %v\n", topic, subs)
	}
}

func (node *Node) AddClient(clientID string) {
	node.ClientIDsMutex.Lock()
	defer node.ClientIDsMutex.Unlock()

	for _, id := range node.ClientIDs {
		if id == clientID {
			return
		}
	}
	node.ClientIDs = append(node.ClientIDs, clientID)
}

func (node *Node) RemoveClient(clientID string) {
	node.ClientIDsMutex.Lock()
	newList := node.ClientIDs[:0]
	for _, id := range node.ClientIDs {
		if id != clientID {
			newList = append(newList, id)
		}
	}
	node.ClientIDs = newList
	node.ClientIDsMutex.Unlock()

	node.clearSubClient(clientID)
	node.UnregisterRpcServicesByClient(clientID)
}

func (node *Node) UnregisterRpcServicesByClient(clientID string) {
	node.RpcFuncsMutex.Lock()
	defer node.RpcFuncsMutex.Unlock()

	for funcName, providers := range node.RpcFuncs {
		if _, ok := providers[clientID]; ok {
			delete(providers, clientID)
			log.Printf("Unregistered RPC: %s by client %s", funcName, clientID)
		}
		if len(providers) == 0 {
			delete(node.RpcFuncs, funcName)
		}
	}
}

func (node *Node) GetClientIDs() []string {
	node.ClientIDsMutex.RLock()
	defer node.ClientIDsMutex.RUnlock()

	copyList := make([]string, len(node.ClientIDs))
	copy(copyList, node.ClientIDs)
	return copyList
}

func (node *Node) PrintClients() {
	node.ClientIDsMutex.RLock()
	defer node.ClientIDsMutex.RUnlock()

	fmt.Printf("Node %s has connected Clients:\n", node.ID)
	if len(node.ClientIDs) == 0 {
		fmt.Println("  None")
		return
	}
	for _, id := range node.ClientIDs {
		fmt.Printf("  - %s\n", id)
	}
}

func (n *Node) RegisterWSServer(server *Server) {
	n.WsServersMutex.Lock()
	defer n.WsServersMutex.Unlock()
	n.WsServers = append(n.WsServers, server)
}

func (n *Node) startHttpServer(wsPort string) {
	server := NewServer(n, wsPort)
	router := SetupRoutes(server)
	err := router.Run(":" + wsPort)
	if err != nil {
		fmt.Printf("Server server failed to start on Port %s: %v\n", wsPort, err)
	}
}
