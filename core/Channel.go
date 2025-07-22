package core

import (
	"Majula/common"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// DebugPrint channel的Debug打印，调试时输出指定信息。
// 参数：name - 调用者名称，message - 要输出的信息。
func (channel *Channel) DebugPrint(name string, message string) {
	if !common.DebugPrint {
		return
	}
	fmt.Printf("{%s: %s} %s\n", channel.ID, name, message)
}

// Connection connection的连接情况
type Connection struct {
	LastRecvTime time.Time
	LastSendTime time.Time
}

// 更新指定节点的最新发送时间，如果不存在则新建。
// 参数：peerID - 节点ID。
func (channel *Channel) updateSendTime(peerID string) {
	now := time.Now()
	channel.ChannelPeerMutex.Lock()
	defer channel.ChannelPeerMutex.Unlock()

	if conn, ok := channel.ChannelPeers[peerID]; ok {
		conn.LastSendTime = now
	} else {
		channel.ChannelPeers[peerID] = &Connection{
			LastSendTime: now,
			LastRecvTime: now,
		}
	}
}

// 更新指定节点的最新接收时间，如果不存在则新建。
// 参数：peerID - 节点ID。
func (channel *Channel) updateRecvTime(peerID string) {
	now := time.Now()
	channel.ChannelPeerMutex.Lock()
	defer channel.ChannelPeerMutex.Unlock()

	if conn, ok := channel.ChannelPeers[peerID]; ok {
		conn.LastRecvTime = now
	} else {
		channel.ChannelPeers[peerID] = &Connection{
			LastRecvTime: now,
			LastSendTime: now,
		}
	}
}

// Channel Channel结构体，表示一个逻辑通道，包含通道ID、所属节点、通道内节点连接等信息。
type Channel struct {
	ID               string
	HNode            *Node
	ChannelPeers     map[string]*Connection // 注意：string指代的是对应连接node的id，而非channelId
	Worker           ChannelWorker
	ChannelPeerMutex sync.Mutex
}

// NewChannel 创建一个新的Channel实例。
// 返回：*Channel 新建的通道对象。
func NewChannel() *Channel {
	ret := Channel{
		ChannelPeers:     make(map[string]*Connection),
		ChannelPeerMutex: sync.Mutex{},
	}
	return &ret
}

// NewChannelFull 创建一个带完整参数的Channel实例。
// 参数：pId - 通道ID，hostNode - 所属节点，assignedWorker - 通道工作者。
// 返回：*Channel 新建的通道对象。
func NewChannelFull(pId string, hostNode *Node, assignedWorker ChannelWorker) *Channel {
	ret := Channel{
		ID:               pId,
		HNode:            hostNode,
		ChannelPeers:     make(map[string]*Connection),
		Worker:           assignedWorker,
		ChannelPeerMutex: sync.Mutex{},
	}
	return &ret
}

// 获取当前通道所属节点的ID。
// 返回：节点ID字符串。
func (channel *Channel) getID() string {
	return channel.HNode.ID
}

// 设置通道的工作者对象。
// 参数：pChannelWorker - 通道工作者。
func (channel *Channel) setChannelWorker(pChannelWorker ChannelWorker) {
	channel.Worker = pChannelWorker
}

// 添加或更新一个通道内的节点连接信息。
// 参数：peerId - 节点ID。
func (channel *Channel) addChannelPeer(peerId string) {
	channel.ChannelPeerMutex.Lock()
	defer channel.ChannelPeerMutex.Unlock()

	now := time.Now()
	if conn, exists := channel.ChannelPeers[peerId]; exists {
		channel.DebugPrint("AddChannel", "Update Channel Peer "+peerId)
		conn.LastRecvTime = now
		conn.LastSendTime = now
	} else {
		channel.DebugPrint("AddChannel", "Add Channel Peer "+peerId)
		channel.ChannelPeers[peerId] = &Connection{
			LastRecvTime: now,
			LastSendTime: now,
		}
	}
}

// 向通道内所有节点广播消息。
// 参数：msg - 要广播的消息。
func (channel *Channel) broadCast(msg *Message) {
	channel.DebugPrint("broadCast", msg.Print())

	channel.ChannelPeerMutex.Lock()
	peers := make([]string, 0, len(channel.ChannelPeers))
	for peerID := range channel.ChannelPeers {
		peers = append(peers, peerID)
	}
	channel.ChannelPeerMutex.Unlock()
	for _, peerID := range peers {
		channel.updateSendTime(peerID)
	}

	go channel.Worker.broadCast(msg)
}

// 向指定节点发送消息。
// 参数：nextHopNodeId - 目标节点ID，msg - 消息内容。
func (channel *Channel) send(nextHopNodeId string, msg *Message) {
	channel.DebugPrint("send", msg.Print())
	channel.updateSendTime(nextHopNodeId)
	go channel.Worker.sendTo(nextHopNodeId, msg)
}

// 处理接收到的消息，根据类型分发到不同的处理函数。
// 参数：sourceNodeId - 消息来源节点ID，msg - 消息内容。
func (channel *Channel) onRecv(sourceNodeId string, msg *Message) {
	channel.DebugPrint("onRecv", msg.Print())
	//fmt.Println(channel.HNode.ID+" onRecv ", msg.Print())
	if sourceNodeId == channel.HNode.ID {
		return
	}
	channel.updateRecvTime(sourceNodeId)
	switch msg.Type {
	case CostRequest:
		go channel.handleCostRequest(sourceNodeId, msg)
	case CostAck:
		go channel.handleCostAck(sourceNodeId, msg)
	case Hello:
		go channel.handleHello(sourceNodeId, msg)
	case HeartBeat:
		go channel.handleHeartbeat(sourceNodeId, msg)
	default:
		go channel.HNode.onRecv(sourceNodeId, msg)
	}
}

// 处理接收到的心跳包消息。
// 参数：sourceNodeId - 消息来源节点ID，msg - 消息内容。
func (channel *Channel) handleHeartbeat(sourceNodeId string, msg *Message) {
	channel.DebugPrint("handleHeartbeat", msg.Print())
	go channel.HNode.onRecv(sourceNodeId, msg)
}

// 处理接收到的Hello消息，添加新节点并发起CostRequest。
// 参数：sourceNodeId - 消息来源节点ID，msg - 消息内容。
func (channel *Channel) handleHello(sourceNodeId string, msg *Message) {
	channel.DebugPrint("handleHello", msg.Print())
	go channel.HNode.onRecv(sourceNodeId, msg)
	channel.addChannelPeer(sourceNodeId)
	timestamp := time.Now().UnixNano()
	costRequest := &Message{
		MessageData: MessageData{
			Type: CostRequest,
			Data: strconv.FormatInt(timestamp, 10),
		},
		From:       channel.HNode.ID,
		To:         sourceNodeId,
		Route:      sourceNodeId,
		TTL:        1,
		Lost:       false,
		LastSender: channel.HNode.ID,
	}
	channel.DebugPrint("handleHello-sendCostRequest", costRequest.Print())
	go channel.send(sourceNodeId, costRequest)
}

// 检查通道内节点的活跃状态，移除长时间未响应的节点，并向活跃节点发送CostRequest。
func (channel *Channel) checkCost() {
	channel.DebugPrint("checkCost", "start the check cost action")
	now := time.Now()
	var expiredPeers []string

	channel.ChannelPeerMutex.Lock()
	for peerID, conn := range channel.ChannelPeers {
		if now.Sub(conn.LastRecvTime) > common.CostCheckTimePeriod*20 {
			expiredPeers = append(expiredPeers, peerID)
		}
	}
	for _, peerID := range expiredPeers {
		delete(channel.ChannelPeers, peerID)
	}
	channel.ChannelPeerMutex.Unlock()

	for peerID := range channel.ChannelPeers {
		timestamp := time.Now().UnixNano()
		msg := &Message{
			MessageData: MessageData{
				Type: CostRequest,
				Data: strconv.FormatInt(timestamp, 10),
			},
			From:       channel.HNode.ID,
			To:         peerID,
			Route:      peerID,
			TTL:        1,
			Lost:       false,
			LastSender: channel.HNode.ID,
		}
		go channel.send(peerID, msg)
	}
	channel.DebugPrint("checkCost", "finish the check cost action")
}

// 处理收到的CostRequest消息，回复CostAck。
// 参数：source - 请求来源节点ID，msg - 消息内容。
func (channel *Channel) handleCostRequest(source string, msg *Message) {
	channel.DebugPrint("handleCostRequest", msg.Print())
	ack := &Message{
		MessageData: MessageData{
			Type: CostAck,
			Data: msg.MessageData.Data,
		},
		From:       channel.HNode.ID,
		To:         source,
		Route:      source,
		TTL:        1,
		Lost:       false,
		LastSender: channel.HNode.ID,
	}
	channel.DebugPrint("handleCostRequest-sendACK", ack.Print())
	go channel.send(source, ack)
}

// 处理收到的CostAck消息，更新链路信息。
// 参数：source - 回复来源节点ID，msg - 消息内容。
func (channel *Channel) handleCostAck(source string, msg *Message) {
	channel.DebugPrint("handleCostAck", msg.Print())
	costTime, err := strconv.ParseInt(msg.MessageData.Data, 10, 64)
	if err != nil {
		return
	}
	now := time.Now().UnixNano()

	cost := now - costTime
	link := Link{
		Source:  channel.HNode.ID,
		Target:  source,
		Cost:    cost,
		Version: channel.HNode.MyLinksVersion,
		Channel: channel.ID,
	}
	go channel.HNode.linkUpdateFromChannel(link)
}

// 处理通道连接状态变化，连接建立时广播Hello消息。
// 参数：worker - 通道工作者，connected - 是否已连接。
func (channel *Channel) onConnectChanged(worker ChannelWorker, connected bool) {
	if connected {
		msg := &Message{
			MessageData: MessageData{
				Type: Hello,
				Data: channel.HNode.ID,
			},
			From:       channel.HNode.ID,
			TTL:        1,
			Lost:       false,
			LastSender: channel.HNode.ID,
		}
		//fmt.Println("[DEBUG] "+channel.HNode.ID+" onConnectChanged", msg.Print())
		channel.DebugPrint("onConnectedChanged", "BroadCast Hello")
		go worker.broadCast(msg)
	}
}

// =====================
// Channel User & Worker
// =====================

// ChannelUser Channel is Channel user
type ChannelUser interface {
	getID() string
	send(nextHopNodeId string, msg *Message)
	broadCast(msg *Message)
	onRecv(sourceNodeId string, msg *Message)
	onConnectChanged(worker ChannelWorker, connected bool)
}
