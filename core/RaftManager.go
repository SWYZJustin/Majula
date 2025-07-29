package core

import (
	"fmt"
	"sync"
)

// RaftManager 管理所有 raft group 及 learner，负责创建、加入、移除等操作
type RaftManager struct {
	RaftGroup      []string
	RaftStubs      map[string]*RaftClient // GroupId -> RaftClient
	RaftStubsMutex sync.RWMutex

	LearnerStubs      map[string]*LearnerClient
	LearnerStubsMutex sync.RWMutex

	RaftPeers      map[string][]string
	RaftPeersMutex sync.RWMutex
}

func NewRaftManager() *RaftManager {
	return &RaftManager{
		RaftGroup:         []string{},
		RaftStubs:         map[string]*RaftClient{},
		RaftStubsMutex:    sync.RWMutex{},
		LearnerStubs:      map[string]*LearnerClient{},
		LearnerStubsMutex: sync.RWMutex{},
		RaftPeers:         make(map[string][]string),
		RaftPeersMutex:    sync.RWMutex{},
	}
}

func (rm *RaftManager) CreateRaftGroup(group string, node *Node, peers []string, dbPath string) (*RaftClient, error) {
	rm.RaftStubsMutex.RLock()
	_, exists := rm.RaftStubs[group]
	rm.RaftStubsMutex.RUnlock()
	if exists {
		return nil, fmt.Errorf("Raft group %s already exists", group)
	}

	raftClient := NewRaftClient(group, node, peers, dbPath)

	rm.RaftStubsMutex.Lock()
	rm.RaftStubs[group] = raftClient
	rm.RaftGroup = append(rm.RaftGroup, group)
	rm.RaftStubsMutex.Unlock()

	rm.RaftPeersMutex.Lock()
	rm.RaftPeers[group] = peers
	rm.RaftPeersMutex.Unlock()

	fmt.Printf("[RaftManager] Created Raft group %s with peers: %v\n", group, peers)
	return raftClient, nil
}

func (rm *RaftManager) CreateLearner(group string, node *Node, dbPath string) (*LearnerClient, error) {
	rm.RaftPeersMutex.RLock()
	peers, ok := rm.RaftPeers[group]
	rm.RaftPeersMutex.RUnlock()
	if !ok {
		return nil, fmt.Errorf("raft group %s does not exist", group)
	}

	params := map[string]interface{}{
		"group":      group,
		"learner_id": node.ID,
	}

	for _, peer := range peers {
		result, ok := node.MakeRpcRequest(peer, "raft", "addLearner", params)
		if !ok {
			continue
		}

		resp := result.(map[string]interface{})
		if success, _ := resp["success"].(bool); success {
			lc := NewLearnerClient(group, node, dbPath)
			rm.LearnerStubsMutex.Lock()
			rm.LearnerStubs[group] = lc
			rm.LearnerStubsMutex.Unlock()
			return lc, nil
		} else if redirect, ok := resp["redirect_to"].(string); ok && redirect != "" {
			result, ok := node.MakeRpcRequest(redirect, "raft", "addLearner", params)
			if ok {
				resp := result.(map[string]interface{})
				if success, _ := resp["success"].(bool); success {
					lc := NewLearnerClient(group, node, dbPath)
					rm.LearnerStubsMutex.Lock()
					rm.LearnerStubs[group] = lc
					rm.LearnerStubsMutex.Unlock()
					return lc, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to join as learner")
}

// 移除 Learner 节点
func (rm *RaftManager) RemoveLearner(group string, node *Node) error {
	rm.RaftPeersMutex.RLock()
	peers, ok := rm.RaftPeers[group]
	rm.RaftPeersMutex.RUnlock()
	if !ok {
		return fmt.Errorf("raft group %s does not exist", group)
	}

	params := map[string]interface{}{
		"group":      group,
		"learner_id": node.ID,
	}

	for _, peer := range peers {
		result, ok := node.MakeRpcRequest(peer, "raft", "removeLearner", params)
		if !ok {
			continue
		}

		resp := result.(map[string]interface{})
		if success, _ := resp["success"].(bool); success {
			rm.LearnerStubsMutex.Lock()
			delete(rm.LearnerStubs, group)
			rm.LearnerStubsMutex.Unlock()
			return nil
		} else if redirect, ok := resp["redirect_to"].(string); ok && redirect != "" {
			result, ok := node.MakeRpcRequest(redirect, "raft", "removeLearner", params)
			if ok {
				resp := result.(map[string]interface{})
				if success, _ := resp["success"].(bool); success {
					rm.LearnerStubsMutex.Lock()
					delete(rm.LearnerStubs, group)
					rm.LearnerStubsMutex.Unlock()
					return nil
				}
			}
		}
	}

	return fmt.Errorf("failed to remove learner from group %s", group)
}

// JoinAsLearner 以 learner 形式加入 raft group
// 参数：group - raft group 名称，node - 本地节点，dbPath - learner 本地存储路径
// 返回：错误信息
func (rm *RaftManager) JoinAsLearner(group string, node *Node, dbPath string) error {
	_, err := rm.CreateLearner(group, node, dbPath)
	return err
}

// LeaveAsLearner 以 learner 形式退出 raft group
// 参数：group - raft group 名称，node - 本地节点
// 返回：错误信息
func (rm *RaftManager) LeaveAsLearner(group string, node *Node) error {
	return rm.RemoveLearner(group, node)
}

type JoinLearnerRequest struct {
	Group     string `json:"group"`
	LearnerID string `json:"learnerId"`
}

type LearnerAddedAck struct {
	Group     string `json:"group"`
	LearnerID string `json:"learnerId"`
	Success   bool   `json:"success"`
}
