package core

import (
	"Majula/common"
	"sync"
)

// RaftLearner 实现Raft Learner角色，只接收日志，不参与投票和选举。
// 主要字段：
//   - ID: 本地client唯一标识
//   - Group: 所属同步组ID
//   - CurrentTerm: 当前任期号
//   - Log: 日志条目
//   - CommitIndex/LastApplied: 日志提交/应用进度
//   - LeaderHint: 当前已知的Leader节点ID
//   - Storage: LevelDB持久化
//   - ApplyCallback: 日志应用回调
//   - 其余为锁、网络通信等
//
// 用法：每个group对应一个LearnerClient实例，用于只读场景。
type RaftLearner struct {
	ID          string         // 本地client唯一ID
	Group       string         // 所属groupID
	CurrentTerm int64          // 当前term
	Log         []RaftLogEntry // 日志条目
	CommitIndex int64          // 已提交日志index
	LastApplied int64          // 已应用日志index
	Mutex       sync.Mutex     // 状态锁
	Node        *Node          // 所属Node

	LeaderHint    string                   // 当前已知的LeaderID
	Storage       *Storage                 // 持久化存储
	ApplyCallback func(entry RaftLogEntry) // 日志应用回调

	// 快照 Learner 相关字段
	IsSnapshotLearner bool  // 是否为快照 Learner
	SnapshotIndex     int64 // 快照对应的日志索引
	SnapshotTerm      int64 // 快照对应的日志任期
	MaxAppliedIndex   int64 // 最大已应用索引（用于快照 Learner）
}

// NewLearnerClient 创建一个新的LearnerClient实例，并从存储加载元数据和日志。
// group: 同步组ID
// node: 所属Node
// dbPath: LevelDB存储路径
// 返回：*RaftLearner
func NewLearnerClient(group string, node *Node, dbPath string) *RaftLearner {
	storage, err := NewStorage(dbPath, node.ID)
	if err != nil {
		Error("Learner存储创建失败", "节点ID=", node.ID, "错误=", err)
		// 使用内存存储作为fallback
		storage = &Storage{
			db:     nil,
			NodeId: node.ID,
		}
	}

	lc := &RaftLearner{
		ID:          node.ID,
		Group:       group,
		CurrentTerm: 0,
		Log:         make([]RaftLogEntry, 0),
		CommitIndex: 0,
		LastApplied: 0,
		Node:        node,
		Storage:     storage,
	}

	term, _, commitIdx, lastApplied, _ := storage.LoadMeta(lc.Group)
	lc.CurrentTerm = term
	lc.CommitIndex = commitIdx
	lc.LastApplied = lastApplied
	lc.LoadLogs()

	if lc.CommitIndex > lc.LastApplied {
		lc.Mutex.Lock()
		lc.applyLogToStateMachine()
		lc.Mutex.Unlock()
	}

	return lc
}

// onRaftMessage 处理收到的Raft消息，仅处理AppendEntries类型。
// group: 组ID
// from: 发送方ID
// to: 接收方ID
// content: 消息内容（序列化的RaftPayload）
func (lc *RaftLearner) onRaftMessage(group, from, to string, content []byte) {
	var payload RaftPayload
	if err := common.UnmarshalAny(content, &payload); err != nil {
		Error("Learner消息反序列化失败", "错误=", err)
		return
	}
	if payload.Type == AppendEntries {
		lc.handleAppendEntries(payload)
	}
}

// handleAppendEntries 处理Leader发来的AppendEntries日志复制请求。
// payload: RaftPayload结构体，包含日志条目、前置日志信息、commitIndex等
// 处理流程：
//  1. 检查term，更新LeaderHint
//  2. 检查日志连续性和冲突，必要时截断本地日志
//  3. 追加新日志并持久化
//  4. 推进commitIndex并应用日志到状态机
//  5. 回复Leader复制结果
func (lc *RaftLearner) handleAppendEntries(payload RaftPayload) {
	lc.Mutex.Lock()
	defer lc.Mutex.Unlock()

	if payload.Term >= lc.CurrentTerm {
		lc.CurrentTerm = payload.Term
		lc.LeaderHint = payload.LeaderId
	}

	lastIndex := int64(len(lc.Log))

	// 快照 Learner 的特殊处理
	if lc.IsSnapshotLearner {
		lc.handleAppendEntriesForSnapshot(payload)
		return
	}

	// 传统 Learner 的处理逻辑
	// 1. 日志过短，直接拒绝
	if payload.PrevLogIndex > lastIndex {
		lc.replyAppendEntries(payload.LeaderId, false, 0, lastIndex+1, payload.InvokeId)
		return
	}

	// 2. 日志冲突检查
	if payload.PrevLogIndex > 0 && lc.Log[payload.PrevLogIndex-1].Term != payload.PrevLogTerm {
		conflictTerm := lc.Log[payload.PrevLogIndex-1].Term
		conflictIndex := payload.PrevLogIndex
		for conflictIndex > 1 && lc.Log[conflictIndex-2].Term == conflictTerm {
			conflictIndex--
		}

		// 删除内存日志
		lc.Log = lc.Log[:conflictIndex-1]
		// 删除 LevelDB 日志（新增）
		_ = lc.Storage.DeleteLogsFrom(lc.Group, conflictIndex)

		lc.replyAppendEntries(payload.LeaderId, false, conflictTerm, conflictIndex, payload.InvokeId)
		return
	}

	// 3. 追加日志 & 持久化
	if len(payload.Entries) > 0 {
		for _, entry := range payload.Entries {
			logIdx := int(entry.Index) - 1
			if logIdx < len(lc.Log) {
				if lc.Log[logIdx].Term != entry.Term {
					// 截断冲突日志
					lc.Log = lc.Log[:logIdx]
					_ = lc.Storage.DeleteLogsFrom(lc.Group, entry.Index) // 删除 LevelDB 冲突日志
				}
			}
			// 追加新日志并持久化
			if int64(len(lc.Log)) < entry.Index {
				lc.Log = append(lc.Log, entry)
				_ = lc.Storage.SaveLog(lc.Group, entry) // **逐条持久化**
			}
		}
	}

	if payload.LeaderCommit > lc.CommitIndex {
		li := int64(len(lc.Log))
		lc.CommitIndex = min(payload.LeaderCommit, li)
		lc.applyLogToStateMachine()

		_ = lc.Storage.SaveMeta(lc.Group, lc.CurrentTerm, "", lc.CommitIndex, lc.LastApplied)
	}

	// 4. 回复 Leader 成功
	lc.replyAppendEntries(payload.LeaderId, true, 0, int64(len(lc.Log)), payload.InvokeId)
}

// handleAppendEntriesForSnapshot 快照 Learner 的特殊处理逻辑
func (lc *RaftLearner) handleAppendEntriesForSnapshot(payload RaftPayload) {
	// 快照 Learner 的核心逻辑：
	// 1. 状态机已经是最新的（通过快照同步）
	// 2. 不需要检查日志连续性
	// 3. 只追加快照索引之后的新日志
	// 4. 回复时返回最大已应用索引

	// 追加新日志（只追加快照索引之后的）
	if len(payload.Entries) > 0 {
		for _, entry := range payload.Entries {
			// 只追加快照索引之后的日志
			if entry.Index > lc.SnapshotIndex {
				logIdx := int(entry.Index) - 1
				if logIdx < len(lc.Log) {
					if lc.Log[logIdx].Term != entry.Term {
						// 截断冲突日志
						lc.Log = lc.Log[:logIdx]
						_ = lc.Storage.DeleteLogsFrom(lc.Group, entry.Index)
					}
				}
				// 追加新日志并持久化
				if int64(len(lc.Log)) < entry.Index {
					lc.Log = append(lc.Log, entry)
					_ = lc.Storage.SaveLog(lc.Group, entry)
				}

				// 更新最大已应用索引
				if entry.Index > lc.MaxAppliedIndex {
					lc.MaxAppliedIndex = entry.Index
				}
			}
		}
	}

	// 推进 CommitIndex（基于状态机状态）
	if payload.LeaderCommit > lc.CommitIndex {
		lc.CommitIndex = payload.LeaderCommit
		// 注意：状态机已经是最新的，不需要重新应用
		_ = lc.Storage.SaveMeta(lc.Group, lc.CurrentTerm, "", lc.CommitIndex, lc.LastApplied)
	}

	// 回复时返回最大已应用索引，不是日志长度
	replyIndex := lc.MaxAppliedIndex
	if replyIndex == 0 {
		replyIndex = lc.SnapshotIndex // 如果没有新日志，返回快照索引
	}
	lc.replyAppendEntries(payload.LeaderId, true, 0, replyIndex, payload.InvokeId)
}

// replyAppendEntries 回复Leader的AppendEntries请求，返回复制结果和冲突信息。
// leaderId: Leader节点ID
// success: 是否复制成功
// conflictTerm/conflictIndex: 冲突日志的term和index（用于加速回退）
// invokeId: 请求唯一标识
func (lc *RaftLearner) replyAppendEntries(leaderId string, success bool, conflictTerm, conflictIndex int64, invokeId uint64) {
	lastIndex := int64(len(lc.Log))
	resp := RaftPayload{
		Type:          AppendEntriesResponse,
		Term:          lc.CurrentTerm,
		Success:       success,
		LeaderId:      leaderId,
		ConflictTerm:  conflictTerm,
		ConflictIndex: conflictIndex,
		CommitIndex:   lastIndex,
		InvokeId:      invokeId,
	}
	respBytes, _ := common.MarshalAny(resp)
	lc.sendToTarget(leaderId, string(respBytes))
}

// applyLogToStateMachine 将已提交日志应用到本地状态机，并持久化元数据。
// 支持业务回调。
func (lc *RaftLearner) applyLogToStateMachine() {
	for lc.LastApplied < lc.CommitIndex {
		lc.LastApplied++
		entry := lc.Log[lc.LastApplied-1]
		cmd := entry.Command
		switch cmd.Op {
		case put:
			_ = lc.Storage.PutState(lc.Group, cmd.Key, cmd.Value)
		case delOp:
			_ = lc.Storage.DeleteState(lc.Group, cmd.Key)
		}
		if lc.ApplyCallback != nil {
			lc.ApplyCallback(entry)
		}
	}

	_ = lc.Storage.SaveMeta(lc.Group, lc.CurrentTerm, "", lc.CommitIndex, lc.LastApplied)
}

// persistLog 持久化最新一条日志到存储。
func (lc *RaftLearner) persistLog() {
	if len(lc.Log) == 0 {
		return
	}
	lastEntry := lc.Log[len(lc.Log)-1]
	_ = lc.Storage.SaveLog(lc.Group, lastEntry)
}

// LoadLogs 从存储加载所有日志到内存。
func (lc *RaftLearner) LoadLogs() {
	logs, err := lc.Storage.LoadLogs(lc.Group)
	if err != nil {
		Error("Learner加载日志失败", "节点ID=", lc.ID, "组=", lc.Group, "错误=", err)
		return
	}
	lc.Log = logs
}

// sendToTarget 发送消息到指定目标节点。
// targetNode: 目标节点ID
// content: 消息内容
func (lc *RaftLearner) sendToTarget(targetNode string, content string) {
	Debug("Learner发送消息", "来源=", lc.ID, "目标=", targetNode, "内容=", content)
	msg := &Message{
		MessageData: MessageData{
			Type: RaftMessage,
			Data: content,
		},
		From:       lc.ID,
		LastSender: lc.ID,
		TTL:        common.DefaultMessageTTL,
	}
	lc.Node.sendTo(targetNode, msg)
}
