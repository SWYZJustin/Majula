package core

import (
	"Majula/common"
	"sync"
)

// LearnerClient 表示一个只学习日志、不参与选举和投票的Raft从节点。
// 负责接收Leader的日志复制，持久化日志并应用到本地状态机。
type LearnerClient struct {
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
}

// NewLearnerClient 创建一个新的LearnerClient实例，并从存储加载元数据和日志。
// group: 同步组ID
// node: 所属Node
// dbPath: LevelDB存储路径
// 返回：*LearnerClient
func NewLearnerClient(group string, node *Node, dbPath string) *LearnerClient {
	storage, err := NewStorage(dbPath, node.ID)
	if err != nil {
		Error("Learner存储创建失败", "节点ID=", node.ID, "错误=", err)
		// 使用内存存储作为fallback
		storage = &Storage{
			db:     nil,
			NodeId: node.ID,
		}
	}

	lc := &LearnerClient{
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
func (lc *LearnerClient) onRaftMessage(group, from, to string, content []byte) {
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
func (lc *LearnerClient) handleAppendEntries(payload RaftPayload) {
	lc.Mutex.Lock()
	defer lc.Mutex.Unlock()

	if payload.Term >= lc.CurrentTerm {
		lc.CurrentTerm = payload.Term
		lc.LeaderHint = payload.LeaderId
	}

	lastIndex := int64(len(lc.Log))

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

// replyAppendEntries 回复Leader的AppendEntries请求，返回复制结果和冲突信息。
// leaderId: Leader节点ID
// success: 是否复制成功
// conflictTerm/conflictIndex: 冲突日志的term和index（用于加速回退）
// invokeId: 请求唯一标识
func (lc *LearnerClient) replyAppendEntries(leaderId string, success bool, conflictTerm, conflictIndex int64, invokeId uint64) {
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
func (lc *LearnerClient) applyLogToStateMachine() {
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
func (lc *LearnerClient) persistLog() {
	if len(lc.Log) == 0 {
		return
	}
	lastEntry := lc.Log[len(lc.Log)-1]
	_ = lc.Storage.SaveLog(lc.Group, lastEntry)
}

// LoadLogs 从存储加载所有日志到内存。
func (lc *LearnerClient) LoadLogs() {
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
func (lc *LearnerClient) sendToTarget(targetNode string, content string) {
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
