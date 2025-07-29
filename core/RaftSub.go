package core

import (
	"Majula/common"
	"fmt"
	"sync/atomic"
	"time"
)

// RaftSubscriptionInfo 记录节点已加入的 Raft group 信息
type RaftSubscriptionInfo struct {
	NodeID     string   `json:"node_id"`     // 节点ID
	RaftGroups []string `json:"raft_groups"` // 加入的 Raft group 列表
}

// floodRaftGroupInfo 洪泛本节点已加入的所有 Raft group 信息到全网
func (node *Node) floodRaftGroupInfo() {
	node.RaftManager.RaftStubsMutex.RLock()
	groups := node.RaftManager.RaftGroup
	node.RaftManager.RaftStubsMutex.RUnlock()

	if len(groups) == 0 {
		return // 没有组，不洪泛
	}

	info := RaftSubscriptionInfo{
		NodeID:     node.ID,
		RaftGroups: groups,
	}

	dataBytes, _ := common.MarshalAny(info)

	msg := &Message{
		MessageData: MessageData{
			Type: RaftTopicSubscribeFlood,
			Data: string(dataBytes),
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

// handleRaftSubscribeFlood 处理收到的 Raft group 洪泛消息，自动补全 group 的 peers
// 参数：msg - 洪泛消息
func (node *Node) handleRaftSubscribeFlood(msg *Message) {
	var info RaftSubscriptionInfo
	if err := common.UnmarshalAny([]byte(msg.MessageData.Data), &info); err != nil {
		node.DebugPrint("handleRaftSubscribeFlood", "invalid payload")
		return
	}

	node.RaftManager.RaftPeersMutex.Lock()
	defer node.RaftManager.RaftPeersMutex.Unlock()

	for _, group := range info.RaftGroups {
		peers := node.RaftManager.RaftPeers[group]

		found := false
		for _, id := range peers {
			if id == info.NodeID {
				found = true
				break
			}
		}
		if !found {
			node.RaftManager.RaftPeers[group] = append(peers, info.NodeID)
		}
	}

	newMsg := *msg
	newMsg.LastSender = node.ID
	newMsg.TTL = 1
	for _, ch := range node.Channels {
		go ch.broadCast(&newMsg)
	}
}

// startRaftSubscriptionFloodLoop 定时洪泛本节点 Raft group 信息
func (node *Node) startRaftSubscriptionFloodLoop() {
	ticker := time.NewTicker(common.SubscribeFloodTicket)
	defer ticker.Stop()
	for {
		select {
		case <-node.HBctx.Done():
			return
		case <-ticker.C:
			node.floodRaftGroupInfo()
		}
	}
}

// PrintRaftGroups 打印当前已知的所有 Raft group 及其 peers
func (node *Node) PrintRaftGroups() {
	node.RaftManager.RaftPeersMutex.RLock()
	defer node.RaftManager.RaftPeersMutex.RUnlock()

	fmt.Printf("\n[%s] 当前已知 Raft 组:\n", node.ID)
	for group, peers := range node.RaftManager.RaftPeers {
		fmt.Printf("  Group '%s': %v\n", group, peers)
	}
}

// publishOnRaftTopic 向指定 group 的所有节点发布消息
// 参数：group - 目标 group，pMessage - 消息内容
func (node *Node) publishOnRaftTopic(group string, pMessage string) {
	node.DebugPrint("publishOnRaftTopic", fmt.Sprintf("group=%s msg=%s", group, pMessage))

	// 1. 本地处理：检查是否在本地组中
	if stub := node.getRaftStub(group); stub != nil {
		go stub.onRaftMessage(group, node.ID, node.ID, []byte(pMessage))
	}

	// 2. 网络组发
	node.RaftManager.RaftPeersMutex.RLock()
	targetNodes, ok := node.RaftManager.RaftPeers[group]
	node.RaftManager.RaftPeersMutex.RUnlock()
	if !ok || len(targetNodes) == 0 {
		return
	}

	groupMap := map[string][]string{}
	for _, nodeID := range targetNodes {
		if nodeID == node.ID {
			continue
		}
		routes, ok := node.RoutingTable[nodeID]
		if !ok || len(routes) == 0 {
			continue
		}
		nextHop := routes[0].nextHopNodeID
		groupMap[nextHop] = append(groupMap[nextHop], nodeID)
	}

	for nextHop, toList := range groupMap {
		if len(toList) == 1 {
			msg := &Message{
				MessageData: MessageData{
					Type: RaftTopicPublish,
					Data: fmt.Sprintf("%s|%s", group, pMessage),
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
					Type:     RaftTopicPublish,
					Data:     fmt.Sprintf("%s|%s", group, pMessage),
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

// handleRaftBundle 处理 Raft group 的 bundle 消息转发
// 参数：originalMsg - 原始消息，group - 目标 group，payload - 消息内容
func (node *Node) handleRaftBundle(originalMsg *Message, group, payload string) {
	newBundle := map[string][]string{}

	for _, realTo := range originalMsg.MessageData.BundleTo {
		if realTo == node.ID {
			if stub := node.getRaftStub(group); stub != nil {
				go stub.onRaftMessage(group, originalMsg.From, node.ID, []byte(payload))
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

	for nextHop, targets := range newBundle {
		copyMsg := *originalMsg
		copyMsg.To = ""
		copyMsg.Route = ""
		copyMsg.MessageData.BundleTo = targets
		node.sendTo(nextHop, &copyMsg)
	}
}

// getRaftStub 获取指定 group 的 RaftClient
func (node *Node) getRaftStub(group string) *RaftClient {
	node.RaftManager.RaftStubsMutex.RLock()
	defer node.RaftManager.RaftStubsMutex.RUnlock()
	return node.RaftManager.RaftStubs[group]
}

// getLearnerStub 获取指定 group 的 LearnerClient
func (node *Node) getLearnerStub(group string) *LearnerClient {
	node.RaftManager.LearnerStubsMutex.RLock()
	defer node.RaftManager.LearnerStubsMutex.RUnlock()
	return node.RaftManager.LearnerStubs[group]
}
