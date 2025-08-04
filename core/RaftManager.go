package core

import (
	"fmt"
	"sync"
)

// RaftManager 管理所有Raft组和学习者，负责创建、加入、移除等操作
// RaftManager Raft管理器结构体，包含Raft组、学习者、对等节点等信息
type RaftManager struct {
	RaftGroup      []string             // Raft组列表
	RaftStubs      map[string]*RaftCore // 组ID到Raft客户端的映射
	RaftStubsMutex sync.RWMutex         // Raft存根互斥锁

	LearnerStubs      map[string]*RaftLearner // 学习者存根映射
	LearnerStubsMutex sync.RWMutex            // 学习者存根互斥锁

	RaftPeers      map[string][]string // 组ID到对等节点列表的映射
	RaftPeersMutex sync.RWMutex        // 对等节点互斥锁
}

// NewRaftManager 创建一个新的Raft管理器实例
func NewRaftManager() *RaftManager {
	return &RaftManager{
		RaftGroup:         []string{},
		RaftStubs:         map[string]*RaftCore{},
		RaftStubsMutex:    sync.RWMutex{},
		LearnerStubs:      map[string]*RaftLearner{},
		LearnerStubsMutex: sync.RWMutex{},
		RaftPeers:         make(map[string][]string),
		RaftPeersMutex:    sync.RWMutex{},
	}
}

// CreateRaftGroup 创建一个新的Raft组
// 参数：group - 组名称，node - 本地节点，peers - 对等节点列表，dbPath - 数据库路径
// 返回：Raft客户端指针和错误信息
func (rm *RaftManager) CreateRaftGroup(group string, node *Node, peers []string, dbPath string) (*RaftCore, error) {
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

	Log("Raft管理器创建Raft组", "组名=", group, "对等节点=", peers)
	return raftClient, nil
}

// CreateLearner 创建一个新的学习者客户端
// 参数：group - 组名称，node - 本地节点，dbPath - 数据库路径
// 返回：学习者客户端指针和错误信息
func (rm *RaftManager) CreateLearner(group string, node *Node, dbPath string) (*RaftLearner, error) {
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

// RemoveLearner 移除学习者节点
// 参数：group - 组名称，node - 本地节点
// 返回：错误信息
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

// JoinAsLearner 以学习者形式加入Raft组
// 参数：group - Raft组名称，node - 本地节点，dbPath - 学习者本地存储路径
// 返回：错误信息
func (rm *RaftManager) JoinAsLearner(group string, node *Node, dbPath string) error {
	_, err := rm.CreateLearner(group, node, dbPath)
	return err
}

// LeaveAsLearner 以学习者身份离开Raft组
func (rm *RaftManager) LeaveAsLearner(group string, node *Node) error {
	rm.LearnerStubsMutex.Lock()
	delete(rm.LearnerStubs, group)
	rm.LearnerStubsMutex.Unlock()

	params := map[string]interface{}{
		"group":      group,
		"learner_id": node.ID,
	}

	rm.RaftPeersMutex.RLock()
	peers, ok := rm.RaftPeers[group]
	rm.RaftPeersMutex.RUnlock()
	if !ok {
		return fmt.Errorf("raft group %s does not exist", group)
	}

	for _, peer := range peers {
		result, ok := node.MakeRpcRequest(peer, "raft", "removeLearner", params)
		if ok {
			resp := result.(map[string]interface{})
			if success, _ := resp["success"].(bool); success {
				return nil
			}
		}
	}

	return fmt.Errorf("failed to remove learner from group %s", group)
}

// CreateLearnerWithFastSync 创建一个新的学习者客户端，使用状态机状态传输快速同步
// 参数：group - 组名称，node - 本地节点，dbPath - 数据库路径
// 返回：学习者客户端指针和错误信息
func (rm *RaftManager) CreateLearnerWithFastSync(group string, node *Node, dbPath string) (*RaftLearner, error) {
	rm.RaftPeersMutex.RLock()
	peers, ok := rm.RaftPeers[group]
	rm.RaftPeersMutex.RUnlock()
	if !ok {
		return nil, fmt.Errorf("raft group %s does not exist", group)
	}

	// 1. 先创建Learner客户端
	lc := NewLearnerClient(group, node, dbPath)

	// 2. 尝试获取状态机快照
	snapshot, err := rm.getStateSnapshotFromLeader(group, node, peers)
	if err != nil {
		Log("获取状态机快照失败，使用传统方式加入", "组名=", group, "错误=", err.Error())
		// 如果获取快照失败，回退到传统方式
		return rm.CreateLearner(group, node, dbPath)
	}

	// 3. 应用状态机快照
	err = rm.applyStateSnapshotToLearner(lc, snapshot)
	if err != nil {
		Log("应用状态机快照失败，使用传统方式加入", "组名=", group, "错误=", err.Error())
		// 如果应用快照失败，回退到传统方式
		return rm.CreateLearner(group, node, dbPath)
	}

	// 4. 添加到Learner列表（使用快照命令）
	params := map[string]interface{}{
		"group":      group,
		"learner_id": node.ID,
	}

	// 从快照中获取索引信息
	if snapshotIndex, ok := snapshot["commit_index"].(float64); ok {
		params["snapshot_index"] = snapshotIndex
	} else {
		Log("快照中缺少commit_index信息，使用传统方式加入", "组名=", group)
		return rm.CreateLearner(group, node, dbPath)
	}

	if snapshotTerm, ok := snapshot["term"].(float64); ok {
		params["snapshot_term"] = snapshotTerm
	} else {
		Log("快照中缺少term信息，使用传统方式加入", "组名=", group)
		return rm.CreateLearner(group, node, dbPath)
	}

	for _, peer := range peers {
		result, ok := node.MakeRpcRequest(peer, "raft", "addLearnerWithSnapshot", params)
		if !ok {
			continue
		}

		resp := result.(map[string]interface{})
		if success, _ := resp["success"].(bool); success {
			rm.LearnerStubsMutex.Lock()
			rm.LearnerStubs[group] = lc
			rm.LearnerStubsMutex.Unlock()

			Log("Learner快速同步成功", "组名=", group, "学习者ID=", node.ID, "快照索引=", int64(snapshot["commit_index"].(float64)))
			return lc, nil
		} else if redirect, ok := resp["redirect_to"].(string); ok && redirect != "" {
			result, ok := node.MakeRpcRequest(redirect, "raft", "addLearnerWithSnapshot", params)
			if ok {
				resp := result.(map[string]interface{})
				if success, _ := resp["success"].(bool); success {
					rm.LearnerStubsMutex.Lock()
					rm.LearnerStubs[group] = lc
					rm.LearnerStubsMutex.Unlock()

					Log("Learner快速同步成功", "组名=", group, "学习者ID=", node.ID, "快照索引=", int64(snapshot["commit_index"].(float64)))
					return lc, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to add learner to group %s", group)
}

// getStateSnapshotFromLeader 从Leader获取状态机快照
func (rm *RaftManager) getStateSnapshotFromLeader(group string, node *Node, peers []string) (map[string]interface{}, error) {
	params := map[string]interface{}{
		"group": group,
	}

	// 尝试从所有peer获取快照
	for _, peer := range peers {
		result, ok := node.MakeRpcRequest(peer, "raft", "getStateSnapshot", params)
		if !ok {
			continue
		}

		resp := result.(map[string]interface{})
		if success, _ := resp["success"].(bool); success {
			if snapshot, ok := resp["snapshot"].(map[string]interface{}); ok {
				return snapshot, nil
			}
		} else if redirect, ok := resp["redirect_to"].(string); ok && redirect != "" {
			result, ok := node.MakeRpcRequest(redirect, "raft", "getStateSnapshot", params)
			if ok {
				resp := result.(map[string]interface{})
				if success, _ := resp["success"].(bool); success {
					if snapshot, ok := resp["snapshot"].(map[string]interface{}); ok {
						return snapshot, nil
					}
				}
			}
		}
	}

	return nil, fmt.Errorf("failed to get state snapshot from any peer")
}

// applyStateSnapshotToLearner 将状态机快照应用到Learner
func (rm *RaftManager) applyStateSnapshotToLearner(lc *RaftLearner, snapshot map[string]interface{}) error {
	// 这里需要调用本地RPC，因为Learner客户端还没有完全初始化
	// 我们可以直接操作存储
	if state, ok := snapshot["state"].(map[string]interface{}); ok {
		// 直接设置状态机状态
		for key, value := range state {
			err := lc.Storage.PutState(snapshot["group"].(string), key, value)
			if err != nil {
				return err
			}
		}

		// 设置快照 Learner 相关字段
		lc.IsSnapshotLearner = true
		if snapshotIndex, ok := snapshot["commit_index"].(float64); ok {
			lc.SnapshotIndex = int64(snapshotIndex)
			lc.MaxAppliedIndex = int64(snapshotIndex) // 初始化为快照索引
		}
		if snapshotTerm, ok := snapshot["term"].(float64); ok {
			lc.SnapshotTerm = int64(snapshotTerm)
		}

		return nil
	}

	return fmt.Errorf("invalid snapshot data")
}

// JoinLearnerRequest 加入学习者请求结构体
type JoinLearnerRequest struct {
	Group     string `json:"group"`     // 组名称
	LearnerID string `json:"learnerId"` // 学习者ID
}

// LearnerAddedAck 学习者添加确认结构体
type LearnerAddedAck struct {
	Group     string `json:"group"`     // 组名称
	LearnerID string `json:"learnerId"` // 学习者ID
	Success   bool   `json:"success"`   // 是否成功
}
