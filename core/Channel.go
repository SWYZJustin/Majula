package core

import (
	"Majula/common"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// channel的Debug打印
func (channel *Channel) DebugPrint(name string, message string) {
	if !common.DebugPrint {
		return
	}
	fmt.Printf("{%s: %s} %s\n", channel.ID, name, message)
}

// connection的连接情况
type Connection struct {
	LastRecvTime time.Time
	LastSendTime time.Time
}

// 更新最新发送时间
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

type Channel struct {
	ID               string
	HNode            *Node
	ChannelPeers     map[string]*Connection // 注意：string指代的是对应连接node的id，而非channelId
	Worker           ChannelWorker
	ChannelPeerMutex sync.Mutex
}

func NewChannel() *Channel {
	ret := Channel{
		ChannelPeers:     make(map[string]*Connection),
		ChannelPeerMutex: sync.Mutex{},
	}
	return &ret
}

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

// 获得node的id
func (channel *Channel) getID() string {
	return channel.HNode.ID
}

func (channel *Channel) setChannelWorker(pChannelWorker ChannelWorker) {
	channel.Worker = pChannelWorker
}

func (channel *Channel) addChannelPeer(peerId string) {
	channel.ChannelPeerMutex.Lock()
	defer channel.ChannelPeerMutex.Unlock()

	now := time.Now()
	if conn, exists := channel.ChannelPeers[peerId]; exists {
		channel.DebugPrint("addChannel", "Update Channel Peer "+peerId)
		conn.LastRecvTime = now
		conn.LastSendTime = now
	} else {
		channel.DebugPrint("addChannel", "Add Channel Peer "+peerId)
		channel.ChannelPeers[peerId] = &Connection{
			LastRecvTime: now,
			LastSendTime: now,
		}
	}
}

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

func (channel *Channel) send(nextHopNodeId string, msg *Message) {
	channel.DebugPrint("send", msg.Print())
	channel.updateSendTime(nextHopNodeId)
	go channel.Worker.sendTo(nextHopNodeId, msg)
}

func (channel *Channel) onRecv(sourceNodeId string, msg *Message) {
	channel.DebugPrint("onRecv", msg.Print())
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

func (channel *Channel) handleHeartbeat(sourceNodeId string, msg *Message) {
	channel.DebugPrint("handleHeartbeat", msg.Print())
	go channel.HNode.onRecv(sourceNodeId, msg)
}

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
		channel.DebugPrint("onConnectedChanged", "BroadCast Hello")
		go worker.broadCast(msg)
	}
}

// =====================
// Channel User & Worker
// =====================

// Channel is Channel user
type ChannelUser interface {
	getID() string
	send(nextHopNodeId string, msg *Message)
	broadCast(msg *Message)
	onRecv(sourceNodeId string, msg *Message)
	onConnectChanged(worker ChannelWorker, connected bool)
}
