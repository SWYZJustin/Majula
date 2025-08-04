package core

import (
	"Majula/common"
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Raft时间常量
const (
	// 选举相关
	ELECTION_TIMEOUT_MIN = 2000 // 选举超时最小值（毫秒）
	ELECTION_TIMEOUT_MAX = 3000 // 选举超时最大值（毫秒）

	// 心跳相关
	HEARTBEAT_INTERVAL = 200 // 心跳间隔（毫秒）

	// 网络通信相关
	REQUEST_VOTE_TIMEOUT = 1000 // RequestVote超时时间（毫秒）

	// 主循环检查间隔
	MAIN_LOOP_INTERVAL = 100 // 主循环检查间隔（毫秒）
)

// RaftRole 表示Raft节点的角色（Follower/Candidate/Leader）。
type RaftRole int

const (
	Follower RaftRole = iota
	Candidate
	Leader
)

// Raft 消息类型（int），常量名与原字符串完全一致
type RaftMsgType int

const (
	RequestVote RaftMsgType = iota
	RequestVoteResponse
	AppendEntries
	AppendEntriesResponse
	ClientCommand
	addLearnerMsg
	removeLearnerMsg
)

// Raft 操作类型（int），常量名与原字符串完全一致
type RaftOpType int

const (
	put RaftOpType = iota
	delOp
	addLearner
	removeLearner
	addLearnerWithSnapshot
)

// RaftClient 实现Raft协议的核心状态与主流程，负责选举、日志复制、状态机应用等。
// 主要字段：
//   - ID: 本地client唯一标识
//   - Group: 所属同步组ID
//   - Role: 当前角色
//   - CurrentTerm: 当前任期号
//   - VotedFor: 当前任期已投票对象
//   - Log: 日志条目
//   - CommitIndex/LastApplied: 日志提交/应用进度
//   - NextIndex/MatchIndex: Leader用于跟踪各节点日志进度
//   - Peers: 静态配置的核心节点
//   - Storage: LevelDB持久化
//   - Learners: 只学习日志的节点
//   - ApplyCallback: 日志应用回调
//   - 其余为定时器、锁、投票统计等
//
// 用法：每个group对应一个RaftClient实例，主流程与GroupSyncTable等解耦。
type RaftClient struct {
	ID             string
	Group          string
	Role           RaftRole
	CurrentTerm    int64
	VotedFor       string
	Log            []RaftLogEntry
	CommitIndex    int64
	LastApplied    int64
	NextIndex      map[string]int64
	MatchIndex     map[string]int64
	Mutex          sync.Mutex
	Node           *Node
	voteResult     map[string]bool
	voteResultLock sync.Mutex

	// 时间相关（模仿Elect.go的设计）
	electionTimeout  int64 // 下次选举超时时间
	heartbeatTimeout int64 // 下次心跳超时时间

	// 控制
	ctx     context.Context
	cancel  context.CancelFunc
	running int32 // 运行状态

	LeaderHint string

	Peers []string // 静态配置的核心节点

	Storage *Storage // LevelDB 持久化

	ApplyCallback func(entry RaftLogEntry)

	invokeCounter atomic.Uint64
	pending       sync.Map
	Learners      sync.Map // 存储 Learner 节点 ID -> struct{}

}

// NewRaftClient 创建一个新的RaftClient实例，并从存储加载元数据和日志。
// group: 同步组ID
// node: 所属Node
// peers: 静态配置的核心节点ID列表
// dbPath: LevelDB存储路径
// 返回：*RaftClient
func NewRaftClient(group string, node *Node, peers []string, dbPath string) *RaftClient {
	storage, err := NewStorage(dbPath, node.ID)
	if err != nil {
		Error("Raft存储创建失败", "节点ID=", node.ID, "错误=", err)
		// 使用内存存储作为备用方案
		storage = &Storage{
			db:     nil,
			NodeId: node.ID,
		}
	}

	rc := &RaftClient{
		ID:          node.ID,
		Group:       group,
		Role:        Follower,
		CurrentTerm: 0,
		VotedFor:    "",
		Log:         make([]RaftLogEntry, 0),
		CommitIndex: 0,
		LastApplied: 0,
		NextIndex:   make(map[string]int64),
		MatchIndex:  make(map[string]int64),
		Node:        node,
		voteResult:  make(map[string]bool),
		Peers:       peers,
		Storage:     storage,
	}

	// 恢复元数据
	term, votedFor, commitIdx, lastApplied, _ := storage.LoadMeta(rc.Group)
	rc.CurrentTerm = term
	rc.VotedFor = votedFor
	rc.CommitIndex = commitIdx
	rc.LastApplied = lastApplied
	rc.LoadLogs()

	rc.Mutex.Lock()

	if rc.CommitIndex > rc.LastApplied {
		rc.applyLogToStateMachine()
	}
	rc.Mutex.Unlock()

	rc.startMainLoop()
	rc.resetElectionTimer()

	return rc
}

// persistTermAndVote 持久化当前term和votedFor到存储。
func (rc *RaftClient) persistTermAndVote() {
	_ = rc.Storage.SaveMeta(rc.Group, rc.CurrentTerm, rc.VotedFor, rc.CommitIndex, rc.LastApplied)
}

// persistLog 持久化最新一条日志到存储。
func (rc *RaftClient) persistLog() {
	if len(rc.Log) == 0 {
		return
	}
	lastEntry := rc.Log[len(rc.Log)-1]
	_ = rc.Storage.SaveLog(rc.Group, lastEntry)
}

// applyLogToStateMachine 将已提交日志应用到状态机，并持久化元数据。
// 支持业务回调。
func (rc *RaftClient) applyLogToStateMachine() {

	for rc.LastApplied < rc.CommitIndex {
		rc.LastApplied++
		entry := rc.Log[rc.LastApplied-1]
		cmd := entry.Command

		switch cmd.Op {
		case put:
			_ = rc.Storage.PutState(rc.Group, cmd.Key, cmd.Value)
		case delOp:
			_ = rc.Storage.DeleteState(rc.Group, cmd.Key)
		case addLearner:
			rc.Learners.Store(cmd.Key, struct{}{})
			rc.NextIndex[cmd.Key] = rc.CommitIndex + 1
			rc.MatchIndex[cmd.Key] = 0
			Log("Raft应用添加学习者命令", "节点ID=", rc.ID, "学习者ID=", cmd.Key)
		case addLearnerWithSnapshot:
			rc.Learners.Store(cmd.Key, struct{}{})
			// 使用快照信息设置索引，而不是当前的 CommitIndex
			rc.NextIndex[cmd.Key] = cmd.SnapshotIndex + 1
			rc.MatchIndex[cmd.Key] = cmd.SnapshotIndex // 快照索引已匹配
			Log("Raft应用添加快照学习者命令", "节点ID=", rc.ID, "学习者ID=", cmd.Key, "快照索引=", cmd.SnapshotIndex)
		case removeLearner:
			rc.Learners.Delete(cmd.Key)
			delete(rc.NextIndex, cmd.Key)
			delete(rc.MatchIndex, cmd.Key)
			Log("Raft应用移除学习者命令", "节点ID=", rc.ID, "学习者ID=", cmd.Key)
		default:
			Error("Raft未知命令", "节点ID=", rc.ID, "命令=", cmd)
		}

		if rc.ApplyCallback != nil {
			rc.ApplyCallback(entry)
		}

		_ = rc.Storage.SaveMeta(rc.Group, rc.CurrentTerm, rc.VotedFor, rc.CommitIndex, rc.LastApplied)
	}
}

// onRaftMessage 处理收到的Raft消息，分发给对应的处理函数。
// group: 组ID
// from: 发送方ID
// to: 接收方ID
// content: 消息内容（序列化的RaftPayload）
func (rc *RaftClient) onRaftMessage(group, from, to string, content []byte) {
	var payload RaftPayload
	if err := common.UnmarshalAny(content, &payload); err != nil {
		Error("Raft消息反序列化失败", "错误=", err)
		return
	}
	switch payload.Type {
	case RequestVote:
		rc.handleRequestVote(group, from, to, &payload)
	case RequestVoteResponse:
		rc.handleRequestVoteResponse(group, from, to, &payload)
	case AppendEntries:
		rc.handleAppendEntries(group, from, to, &payload)
	case AppendEntriesResponse:
		rc.handleAppendEntriesResponse(group, from, to, &payload)

	case ClientCommand:
		var cmdPayload ClientForwardPayload
		if err := common.UnmarshalAny(content, &cmdPayload); err == nil {
			var curRole RaftRole
			rc.Mutex.Lock()
			curRole = rc.Role
			rc.Mutex.Unlock()
			if curRole == Leader {
				rc.ProposeCommand(cmdPayload.Cmd)
				return
			}

			if cmdPayload.Forwarded {
				Error("Raft请求已转发过一次，拒绝", "节点ID=", rc.ID)
				return
			}

			if rc.LeaderHint != "" {
				Log("Raft转发请求到领导者", "节点ID=", rc.ID, "领导者ID=", rc.LeaderHint)
				rc.forwardToLeader(rc.LeaderHint, cmdPayload.Cmd, true, cmdPayload.OriginId)
			} else {
				Error("Raft未知领导者，无法转发", "节点ID=", rc.ID)
			}
		}

	default:
		Error("Raft未知消息类型", "类型=", payload.Type)
	}
}

// handleRequestVote 处理RequestVote投票请求。
// group: 组ID
// from: 请求方ID
// to: 接收方ID
// payload: 投票请求内容
func (rc *RaftClient) handleRequestVote(group, from, to string, payload *RaftPayload) {
	rc.Mutex.Lock()
	defer rc.Mutex.Unlock()
	Debug("Raft处理投票请求", "节点ID=", rc.ID, "来源=", from, "请求任期=", payload.Term, "当前任期=", rc.CurrentTerm)

	voteGranted := false
	if payload.Term < rc.CurrentTerm {
		voteGranted = false
	} else {
		if payload.Term > rc.CurrentTerm {
			Log("Raft任期变更", "节点ID=", rc.ID, "原任期=", rc.CurrentTerm, "新任期=", payload.Term)
			rc.CurrentTerm = payload.Term
			rc.VotedFor = ""
			rc.Role = Follower
			rc.persistTermAndVote()
		}
		if (rc.VotedFor == "" || rc.VotedFor == payload.CandidateId) && rc.isUpToDate(payload.PrevLogIndex, payload.PrevLogTerm) {
			Log("Raft投票给候选人", "节点ID=", rc.ID, "候选人ID=", payload.CandidateId, "任期=", rc.CurrentTerm)
			rc.VotedFor = payload.CandidateId
			voteGranted = true
			rc.persistTermAndVote()
			rc.resetElectionTimer()
		}
	}

	resp := RaftPayload{
		Type:        RequestVoteResponse,
		Term:        rc.CurrentTerm,
		VoteGranted: voteGranted,
		CandidateId: payload.CandidateId,
		SenderId:    rc.ID,
		Group:       group,
		InvokeId:    payload.InvokeId,
	}
	respBytes, _ := common.MarshalAny(resp)
	rc.sendToTarget(payload.CandidateId, string(respBytes))
}

// handleRequestVoteResponse 处理RequestVote响应。
// group: 组ID
// from: 响应方ID
// to: 接收方ID
// payload: 响应内容
func (rc *RaftClient) handleRequestVoteResponse(group, from, to string, payload *RaftPayload) {
	if ch, ok := rc.pending.Load(payload.InvokeId); ok {
		ch.(chan *RaftPayload) <- payload
		rc.pending.Delete(payload.InvokeId)
	}

	rc.Mutex.Lock()
	defer rc.Mutex.Unlock()
	Debug("Raft处理投票响应", "节点ID=", rc.ID, "来源=", from, "响应任期=", payload.Term, "当前任期=", rc.CurrentTerm, "投票结果=", payload.VoteGranted)

	if payload.Term < rc.CurrentTerm {
		return
	}
	if payload.Term > rc.CurrentTerm {
		Log("Raft任期变更", "节点ID=", rc.ID, "原任期=", rc.CurrentTerm, "新任期=", payload.Term)
		rc.CurrentTerm = payload.Term
		rc.VotedFor = ""
		rc.Role = Follower
		rc.persistTermAndVote()
		rc.resetElectionTimer()
		return
	}

	voter := payload.SenderId

	rc.recordVote(voter, payload.VoteGranted)

	if rc.Role == Candidate && rc.hasMajorityVotes() {
		rc.switchToLeader()
		Log("Raft成为领导者", "节点ID=", rc.ID, "任期=", rc.CurrentTerm)
		lastIndex, _ := rc.getLastLogIndex()
		for _, peer := range rc.getAllPeers() {
			if peer == rc.ID {
				continue
			}
			rc.NextIndex[peer] = lastIndex + 1
			rc.MatchIndex[peer] = 0
		}
	}
}

// getLastLogIndex 获取本地日志的最后一条的index和term。
// 返回：index, term
func (rc *RaftClient) getLastLogIndex() (int64, int64) {
	if len(rc.Log) == 0 {
		return 0, 0
	}
	lastEntry := rc.Log[len(rc.Log)-1]
	return lastEntry.Index, lastEntry.Term
}

// resetVoteResult 重置本地投票统计。
func (rc *RaftClient) resetVoteResult() {
	rc.voteResultLock.Lock()
	defer rc.voteResultLock.Unlock()

	rc.voteResult = make(map[string]bool)

	rc.voteResult[rc.ID] = true
}

// startElection 发起新一轮选举，向所有peer发送RequestVote。
func (rc *RaftClient) startElection() {
	rc.Mutex.Lock()
	if rc.Role == Leader {
		rc.Mutex.Unlock()
		return
	}
	rc.Mutex.Unlock()
	rc.resetVoteResult()
	rc.Mutex.Lock()
	rc.switchToCandidate()
	term := rc.CurrentTerm
	lastLogIndex, lastLogTerm := rc.getLastLogIndex()
	rc.Mutex.Unlock()

	Log("Raft开始选举", "节点ID=", rc.ID, "任期=", term)

	payload := RaftPayload{
		Type:         RequestVote,
		Term:         term,
		CandidateId:  rc.ID,
		PrevLogIndex: lastLogIndex,
		PrevLogTerm:  lastLogTerm,
		Group:        rc.Group,
	}
	for _, peer := range rc.Peers {
		if peer == rc.ID {
			continue
		}
		go func(peer string) {
			_, err := rc.sendWithInvokeId(peer, &payload, time.Duration(REQUEST_VOTE_TIMEOUT)*time.Millisecond)
			if err != nil {
				Error("Raft投票请求失败", "节点ID=", rc.ID, "目标节点=", peer, "错误=", err)
			}
		}(peer)
	}

}

// recordVote 记录收到的投票结果。
func (rc *RaftClient) recordVote(from string, granted bool) {
	rc.voteResultLock.Lock()
	defer rc.voteResultLock.Unlock()
	if rc.voteResult == nil {
		rc.voteResult = make(map[string]bool)
	}
	rc.voteResult[from] = granted
}

// getAllPeers 获取所有静态配置的核心节点ID。
func (rc *RaftClient) getAllPeers() []string {
	return rc.Peers
}

// broadcastHeartbeat 向所有节点和Learner广播心跳（AppendEntries）。
func (rc *RaftClient) broadcastHeartbeat() {
	rc.Mutex.Lock()
	term := rc.CurrentTerm
	commitIdx := rc.CommitIndex
	peers := rc.getAllPeers()
	rc.Mutex.Unlock()

	for _, peer := range peers {
		if peer == rc.ID {
			continue
		}
		go rc.sendHeartbeat(peer, term, commitIdx)
	}

	rc.Learners.Range(func(key, _ interface{}) bool {
		learnerID := key.(string)
		go rc.sendHeartbeat(learnerID, term, commitIdx)
		return true
	})
}

// sendHeartbeat 向指定节点发送心跳（AppendEntries）。
func (rc *RaftClient) sendHeartbeat(nodeID string, term, commitIdx int64) {
	rc.Mutex.Lock()
	prevLogIndex := rc.NextIndex[nodeID] - 1
	prevLogTerm := int64(0)
	if prevLogIndex > 0 && prevLogIndex <= int64(len(rc.Log)) {
		prevLogTerm = rc.Log[prevLogIndex-1].Term
	}
	rc.Mutex.Unlock()

	payload := RaftPayload{
		Type:         AppendEntries,
		Term:         term,
		LeaderId:     rc.ID,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  prevLogTerm,
		Entries:      []RaftLogEntry{},
		LeaderCommit: commitIdx,
		Group:        rc.Group,
	}

	payloadBytes, _ := common.MarshalAny(payload)
	go rc.sendToTarget(nodeID, string(payloadBytes))
}

// handleAppendEntries 处理Leader发来的AppendEntries日志复制/心跳请求。
// group: 组ID
// from: LeaderID
// to: 本节点ID
// payload: 日志复制内容
func (rc *RaftClient) handleAppendEntries(group, from, to string, payload *RaftPayload) {
	rc.Mutex.Lock()
	defer func() {
		rc.Mutex.Unlock()
	}()

	Debug("Raft处理追加条目", "节点ID=", rc.ID, "来源=", from, "请求任期=", payload.Term, "当前任期=", rc.CurrentTerm, "条目数=", len(payload.Entries), "心跳=", len(payload.Entries) == 0)

	if payload.Term < rc.CurrentTerm {
		rc.replyAppendEntries(payload.LeaderId, false, 0, 0, payload.InvokeId)
		return
	}

	if payload.Term > rc.CurrentTerm {
		rc.switchToFollower(payload.Term)
	}

	rc.LeaderHint = payload.LeaderId

	lastIndex, _ := rc.getLastLogIndex()
	if payload.PrevLogIndex > lastIndex {
		rc.replyAppendEntries(payload.LeaderId, false, 0, lastIndex+1, payload.InvokeId)
		rc.resetElectionTimer()
		return
	}

	if payload.PrevLogIndex > 0 && rc.Log[payload.PrevLogIndex-1].Term != payload.PrevLogTerm {
		conflictTerm := rc.Log[payload.PrevLogIndex-1].Term
		conflictIndex := payload.PrevLogIndex
		for conflictIndex > 1 && rc.Log[conflictIndex-2].Term == conflictTerm {
			conflictIndex--
		}

		rc.Log = rc.Log[:conflictIndex-1]
		_ = rc.Storage.DeleteLogsFrom(rc.Group, conflictIndex)

		rc.replyAppendEntries(payload.LeaderId, false, conflictTerm, conflictIndex, payload.InvokeId)
		rc.resetElectionTimer()
		return
	}

	if len(payload.Entries) > 0 {
		Log("Raft追加日志", "节点ID=", rc.ID, "条目数=", len(payload.Entries), "前置日志索引=", payload.PrevLogIndex)

		for _, entry := range payload.Entries {
			logIdx := int(entry.Index) - 1
			if logIdx < len(rc.Log) {
				if rc.Log[logIdx].Term != entry.Term {
					rc.Log = rc.Log[:logIdx]
				}
			}
			if int64(len(rc.Log)) < entry.Index {
				rc.Log = append(rc.Log, entry)
				_ = rc.Storage.SaveLog(rc.Group, entry)
			}
		}
	}

	if payload.LeaderCommit > rc.CommitIndex {
		li := int64(len(rc.Log))
		rc.CommitIndex = min(payload.LeaderCommit, li)
		rc.applyLogToStateMachine()

		_ = rc.Storage.SaveMeta(rc.Group, rc.CurrentTerm, rc.VotedFor, rc.CommitIndex, rc.LastApplied)
	}

	lastIndex, _ = rc.getLastLogIndex()
	rc.replyAppendEntries(payload.LeaderId, true, 0, lastIndex, payload.InvokeId)
	rc.resetElectionTimer()
}

// replyAppendEntries 回复Leader的AppendEntries请求，返回复制结果和冲突信息。
// leaderId: Leader节点ID
// success: 是否复制成功
// conflictTerm/conflictIndex: 冲突日志的term和index
// invokeId: 请求唯一标识
func (rc *RaftClient) replyAppendEntries(leaderId string, success bool, conflictTerm, conflictIndex int64, invokeId uint64) {
	lastIndex, _ := rc.getLastLogIndex()
	resp := RaftPayload{
		Type:          AppendEntriesResponse,
		Term:          rc.CurrentTerm,
		Success:       success,
		LeaderId:      leaderId,
		ConflictTerm:  conflictTerm,
		ConflictIndex: conflictIndex,
		CommitIndex:   lastIndex,
		InvokeId:      invokeId,
		Group:         rc.Group,
	}
	respBytes, _ := common.MarshalAny(resp)
	rc.sendToTarget(leaderId, string(respBytes))
}

// checkLogMatch 检查本地日志与Leader的前置日志是否匹配。
// prevLogIndex: 前置日志index
// prevLogTerm: 前置日志term
// 返回：是否匹配
func (rc *RaftClient) checkLogMatch(prevLogIndex, prevLogTerm int64) bool {
	if prevLogIndex == 0 {
		return true
	}
	if prevLogIndex < 0 || prevLogIndex > int64(len(rc.Log)) {
		return false
	}
	return rc.Log[prevLogIndex-1].Term == prevLogTerm
}

// handleAppendEntriesResponse 处理AppendEntries响应。
// group: 组ID
// from: 响应方ID
// to: 接收方ID
// payload: 响应内容
func (rc *RaftClient) handleAppendEntriesResponse(group, from, to string, payload *RaftPayload) {
	if ch, ok := rc.pending.Load(payload.InvokeId); ok {
		ch.(chan *RaftPayload) <- payload
		rc.pending.Delete(payload.InvokeId)
	}

	rc.Mutex.Lock()
	defer rc.Mutex.Unlock()

	Debug("Raft处理追加条目响应", "节点ID=", rc.ID, "来源=", from, "响应任期=", payload.Term, "当前任期=", rc.CurrentTerm, "成功=", payload.Success)

	if payload.Term > rc.CurrentTerm {
		rc.switchToFollower(payload.Term)
		return
	}

	if rc.Role != Leader || payload.Term != rc.CurrentTerm {
		return
	}

	if payload.Success {
		if payload.CommitIndex > 0 {
			rc.MatchIndex[from] = payload.CommitIndex
			rc.NextIndex[from] = payload.CommitIndex + 1
		} else {
			rc.MatchIndex[from] = rc.NextIndex[from] - 1
			rc.NextIndex[from] = rc.MatchIndex[from] + 1
		}
		rc.advanceCommitIndex()
	} else {
		if payload.ConflictTerm != 0 {
			lastIndexOfTerm := rc.findLastIndexOfTerm(payload.ConflictTerm)
			if lastIndexOfTerm > 0 {
				rc.NextIndex[from] = lastIndexOfTerm + 1
			} else {
				rc.NextIndex[from] = payload.ConflictIndex
			}
		} else {
			if rc.NextIndex[from] > 1 {
				rc.NextIndex[from]--
			}
		}

		go rc.sendAppendEntriesTo(from)
	}
}

// findLastIndexOfTerm 查找本地日志中指定term的最后一个index。
// term: 目标term
// 返回：最后一个index
func (rc *RaftClient) findLastIndexOfTerm(term int64) int64 {
	for i := int64(len(rc.Log)) - 1; i >= 0; i-- {
		if rc.Log[i].Term == term {
			return rc.Log[i].Index
		}
	}
	return 0
}

// sendAppendEntriesTo 立即向指定节点发送AppendEntries。
// peer: 目标节点ID
func (rc *RaftClient) sendAppendEntriesTo(peer string) {
	rc.Mutex.Lock()
	term := rc.CurrentTerm
	nextIdx := rc.NextIndex[peer]
	commitIdx := rc.CommitIndex

	var entries []RaftLogEntry
	if nextIdx <= int64(len(rc.Log)) {
		entries = append([]RaftLogEntry{}, rc.Log[nextIdx-1:]...)
	}

	prevLogIndex := nextIdx - 1
	prevLogTerm := int64(0)
	if prevLogIndex > 0 {
		prevLogTerm = rc.Log[prevLogIndex-1].Term
	}
	rc.Mutex.Unlock()

	payload := RaftPayload{
		Type:         AppendEntries,
		Term:         term,
		LeaderId:     rc.ID,
		PrevLogIndex: prevLogIndex,
		PrevLogTerm:  prevLogTerm,
		Entries:      entries,
		LeaderCommit: commitIdx,
		Group:        rc.Group,
	}
	go func(peer string, payload RaftPayload) {
		_, err := rc.sendWithInvokeId(peer, &payload, 200*time.Millisecond)
		if err != nil {
			Error("Raft追加条目发送失败", "节点ID=", rc.ID, "目标节点=", peer, "错误=", err)
		}
	}(peer, payload)
}

// advanceCommitIndex 推进commitIndex并应用日志到状态机。
func (rc *RaftClient) advanceCommitIndex() {

	matchIndexes := make([]int64, 0, len(rc.Peers))
	lastIndex, _ := rc.getLastLogIndex()
	matchIndexes = append(matchIndexes, lastIndex)

	for _, peer := range rc.Peers {
		if peer == rc.ID {
			continue
		}
		if idx, ok := rc.MatchIndex[peer]; ok {
			matchIndexes = append(matchIndexes, idx)
		}
	}

	mid := len(matchIndexes) / 2
	N := quickSelect(matchIndexes, mid)

	if N > rc.CommitIndex && N > 0 && rc.Log[N-1].Term == rc.CurrentTerm {
		Log("Raft推进提交索引", "节点ID=", rc.ID, "原提交索引=", rc.CommitIndex, "新提交索引=", N)
		rc.CommitIndex = N
		rc.applyLogToStateMachine()
	}
}

func quickSelect(arr []int64, k int) int64 {
	if len(arr) == 1 {
		return arr[0]
	}
	pivot := arr[rand.Intn(len(arr))]
	lows, highs, pivots := []int64{}, []int64{}, []int64{}
	for _, v := range arr {
		if v < pivot {
			lows = append(lows, v)
		} else if v > pivot {
			highs = append(highs, v)
		} else {
			pivots = append(pivots, v)
		}
	}
	if k < len(lows) {
		return quickSelect(lows, k)
	} else if k < len(lows)+len(pivots) {
		return pivot
	} else {
		return quickSelect(highs, k-len(lows)-len(pivots))
	}
}

// isUpToDate 判断候选人日志是否不比自己旧。
// lastLogIndex: 候选人最后日志index
// lastLogTerm: 候选人最后日志term
// 返回：是否最新
func (rc *RaftClient) isUpToDate(lastLogIndex, lastLogTerm int64) bool {
	myLastIndex, myLastTerm := rc.getLastLogIndex()

	if lastLogTerm > myLastTerm {
		return true
	}
	if lastLogTerm == myLastTerm && lastLogIndex >= myLastIndex {
		return true
	}
	return false
}

// HandleClientRequest 处理业务层发来的写入请求。
// cmd: RaftCommand结构体
func (rc *RaftClient) HandleClientRequest(cmd RaftCommand) {
	rc.Mutex.Lock()
	role := rc.Role
	leaderHint := rc.LeaderHint
	rc.Mutex.Unlock()

	if role == Leader {
		rc.ProposeCommand(cmd)
	} else {
		if leaderHint != "" {
			Log("Raft重定向客户端到领导者", "节点ID=", rc.ID, "领导者ID=", leaderHint)
			rc.forwardToLeader(leaderHint, cmd, false, rc.ID)
		} else {
			Error("Raft无领导者信息，拒绝请求", "节点ID=", rc.ID)
		}
	}
}

type ClientForwardPayload struct {
	Type      RaftMsgType
	Cmd       RaftCommand
	Forwarded bool
	OriginId  string
}

// forwardToLeader 将客户端请求转发给Leader。
// leaderId: Leader节点ID
// cmd: RaftCommand
// forwarded: 是否已转发过
// originId: 原始请求发起者ID
func (rc *RaftClient) forwardToLeader(leaderId string, cmd RaftCommand, forwarded bool, originId string) {
	payload := ClientForwardPayload{
		Type:      ClientCommand,
		Cmd:       cmd,
		Forwarded: forwarded,
		OriginId:  originId,
	}
	data, _ := common.MarshalAny(payload)

	rc.sendToTarget(leaderId, string(data))
}

// ProposeCommand 由Leader发起，将命令追加到本地日志并同步到其他节点。
// cmd: RaftCommand
func (rc *RaftClient) ProposeCommand(cmd RaftCommand) {
	rc.Mutex.Lock()

	if rc.Role != Leader {
		Error("Raft拒绝命令，不是领导者", "节点ID=", rc.ID)
		rc.Mutex.Unlock()
		return
	}

	lastIndex, _ := rc.getLastLogIndex()
	entry := RaftLogEntry{
		Index:   lastIndex + 1,
		Term:    rc.CurrentTerm,
		Command: cmd,
	}

	rc.Log = append(rc.Log, entry)
	rc.persistLog()

	// 准备好对等节点和学习者节点的快照
	peers := make([]string, 0, len(rc.Peers))
	for _, peer := range rc.Peers {
		if peer != rc.ID {
			peers = append(peers, peer)
		}
	}

	learners := []string{}
	rc.Learners.Range(func(key, _ interface{}) bool {
		learner := key.(string)
		learners = append(learners, learner)
		return true
	})

	// 状态修改完成，释放锁
	rc.Mutex.Unlock()

	// 异步发送追加条目
	for _, peer := range peers {
		go rc.sendAppendEntriesTo(peer)
	}

	for _, learner := range learners {
		go rc.sendAppendEntriesTo(learner)
	}
}

// startMainLoop 启动主循环
func (rc *RaftClient) startMainLoop() {
	if !atomic.CompareAndSwapInt32(&rc.running, 0, 1) {
		return // 已经在运行
	}

	rc.ctx, rc.cancel = context.WithCancel(context.Background())

	go func() {
		for {
			select {
			case <-rc.ctx.Done():
				Debug("调试信息", "节点ID=", rc.ID)
				return
			default:
				rc.processTimers()
				time.Sleep(time.Duration(MAIN_LOOP_INTERVAL) * time.Millisecond)
			}
		}
	}()
}

// stopMainLoop 停止主循环
func (rc *RaftClient) stopMainLoop() {
	if atomic.CompareAndSwapInt32(&rc.running, 1, 0) {
		if rc.cancel != nil {
			rc.cancel()
		}
	}
}

func (rc *RaftClient) processTimers() {
	var role RaftRole
	var now, heartbeatTimeout, electionTimeout int64

	// 仅用于读取共享状态
	Debug("调试信息", "节点ID=", rc.ID)

	rc.Mutex.Lock()
	role = rc.Role
	now = time.Now().UnixMilli()
	heartbeatTimeout = rc.heartbeatTimeout
	electionTimeout = rc.electionTimeout
	rc.Mutex.Unlock()

	Debug("Raft处理定时器", "节点ID=", rc.ID, "角色=", rc.Role, "当前时间=", now, "心跳超时=", rc.heartbeatTimeout, "选举超时=", rc.electionTimeout)

	if role != Leader && now+60000 < electionTimeout {
		Debug("Raft调试信息", "节点ID=", rc.ID, "当前时间=", now, "超时时间=", electionTimeout)
	}
	if role != Leader && now >= electionTimeout {
		Log("Raft选举超时，开始选举", "节点ID=", rc.ID, "当前时间=", now, "超时时间=", electionTimeout)
		go rc.startElection()
	}

	if role == Leader && now >= heartbeatTimeout {
		Log("Raft心跳超时，广播心跳", "节点ID=", rc.ID, "当前时间=", now, "下次心跳=", heartbeatTimeout)
		go rc.broadcastHeartbeat()
		rc.resetHeartbeatTimer()
		Debug("Raft心跳定时器重置", "节点ID=", rc.ID, "下次心跳=", rc.heartbeatTimeout)
	}
}

// resetElectionTimer 重置选举定时器
func (rc *RaftClient) resetElectionTimer() {
	timeout := ELECTION_TIMEOUT_MIN + randInt(0, ELECTION_TIMEOUT_MAX-ELECTION_TIMEOUT_MIN)
	rc.electionTimeout = time.Now().UnixMilli() + int64(timeout)
	Debug("Raft重置选举定时器", "节点ID=", rc.ID, "超时时间=", timeout, "毫秒")
}

// resetHeartbeatTimer 重置心跳定时器
func (rc *RaftClient) resetHeartbeatTimer() {
	interval := HEARTBEAT_INTERVAL
	rc.heartbeatTimeout = time.Now().UnixMilli() + int64(interval)
	Debug("Raft重置心跳定时器", "节点ID=", rc.ID, "间隔时间=", interval, "毫秒")
}

func randInt(min, max int) int {
	return min + int(time.Now().UnixNano()%int64(max-min+1))
}

// hasMajorityVotes 判断当前投票结果是否获得多数派。
// 返回：是否过半
func (rc *RaftClient) hasMajorityVotes() bool {
	rc.voteResultLock.Lock()
	defer rc.voteResultLock.Unlock()
	voteCount := 0
	for _, granted := range rc.voteResult {
		if granted {
			voteCount++
		}
	}

	majority := len(rc.Peers)/2 + 1
	return voteCount >= majority
}

// RaftLogEntry 表示一条Raft日志。
type RaftLogEntry struct {
	Index   int64
	Term    int64
	Command RaftCommand
}

// RaftCommand Raft命令结构体，用于客户端请求和日志条目
type RaftCommand struct {
	Op    RaftOpType
	Key   string
	Value interface{}
	// 快照 Learner 相关字段
	SnapshotIndex int64 // 快照对应的日志索引
	SnapshotTerm  int64 // 快照对应的日志任期
}

// RaftPayload 表示Raft消息的载体。
type RaftPayload struct {
	Type          RaftMsgType // 消息类型
	Term          int64       // 当前term
	LeaderId      string      // leader节点ID
	CandidateId   string      // candidate节点ID
	SenderId      string
	PrevLogIndex  int64          // 上一条日志的index
	PrevLogTerm   int64          // 上一条日志的term
	Entries       []RaftLogEntry // 日志条目
	LeaderCommit  int64          // leader已提交的最大日志index
	VoteGranted   bool           // 投票响应
	Success       bool           // 日志复制响应
	CommitIndex   int64          // 当前已提交日志index
	LastApplied   int64          // 当前已应用日志index
	Group         string
	ConflictTerm  int64
	ConflictIndex int64
	InvokeId      uint64
}

// sendToTarget 发送消息到指定目标节点。
// targetNode: 目标节点ID
// content: 消息内容
func (rc *RaftClient) sendToTarget(targetNode string, content string) {
	Debug("Raft发送消息", "来源=", rc.ID, "目标=", targetNode, "内容=", content)
	msg := &Message{
		MessageData: MessageData{
			Type: RaftMessage,
			Data: content,
		},
		From:       rc.ID,
		LastSender: rc.ID,
		TTL:        common.DefaultMessageTTL,
	}
	rc.Node.sendTo(targetNode, msg)
}

// sendWithInvokeId 发送带唯一InvokeId的消息并等待响应。
// peer: 目标节点ID
// payload: RaftPayload
// timeout: 超时时间
// 返回：响应RaftPayload和错误信息
func (rc *RaftClient) sendWithInvokeId(peer string, payload *RaftPayload, timeout time.Duration) (*RaftPayload, error) {
	// 生成调用ID
	currentRole := rc.Role
	invokeId := rc.invokeCounter.Add(1)
	payload.InvokeId = invokeId

	// 注册等待通道
	ch := make(chan *RaftPayload, 1)
	rc.pending.Store(invokeId, ch)
	defer rc.pending.Delete(invokeId)

	// 序列化并发送
	data, _ := common.MarshalAny(payload)
	for retries := 0; retries < 3; retries++ {
		if rc.Role != currentRole {
			return nil, fmt.Errorf("[Raft][%s] Role changed, stop retry", rc.ID)
		}
		rc.sendToTarget(peer, string(data))

		select {
		case resp := <-ch:
			return resp, nil
		case <-time.After(timeout):
			if rc.Role != currentRole {
				break
			}
			Error("Raft等待响应超时", "节点ID=", rc.ID, "目标节点=", peer, "重试次数=", retries+1)
		}
	}
	return nil, fmt.Errorf("[Raft][%s] No response from %s after retries or role change", rc.ID, peer)
}

func (rc *RaftClient) sendToAll(content string) {
	for _, peer := range rc.Peers {
		if peer == rc.ID {
			continue
		}
		go rc.sendToTarget(peer, content)
	}
}

// switchToFollower 切换为Follower角色。
// term: 新的term
func (rc *RaftClient) switchToFollower(term int64) {
	Log("Raft切换到跟随者", "节点ID=", rc.ID, "任期=", term)
	rc.CurrentTerm = term
	rc.Role = Follower
	rc.VotedFor = ""
	rc.persistTermAndVote()

	rc.resetElectionTimer()
}

// switchToCandidate 切换为Candidate角色并自增term。
func (rc *RaftClient) switchToCandidate() {
	Log("Raft切换到候选人", "节点ID=", rc.ID, "任期=", rc.CurrentTerm+1)
	rc.Role = Candidate
	rc.CurrentTerm++
	rc.VotedFor = rc.ID
	rc.persistTermAndVote()
	rc.LeaderHint = ""

	rc.resetElectionTimer()
}

// switchToLeader 切换为Leader角色，初始化相关状态。
func (rc *RaftClient) switchToLeader() {
	Log("Raft切换到领导者", "节点ID=", rc.ID, "任期=", rc.CurrentTerm)
	rc.Role = Leader
	rc.disableElectionTimer()
	rc.resetHeartbeatTimer()

	lastIndex, _ := rc.getLastLogIndex()

	// 初始化对等节点的NextIndex和MatchIndex
	for _, peer := range rc.Peers {
		if peer == rc.ID {
			continue
		}
		rc.NextIndex[peer] = lastIndex + 1
		rc.MatchIndex[peer] = 0
	}

	rc.Learners.Range(func(key, _ interface{}) bool {
		learnerID := key.(string)
		if learnerID != rc.ID { // 避免初始化自己
			rc.NextIndex[learnerID] = lastIndex + 1
			rc.MatchIndex[learnerID] = 0
		}
		return true
	})
	go rc.broadcastHeartbeat()
}

// LoadLogs 从存储加载所有日志到内存。
func (rc *RaftClient) LoadLogs() {
	logs, err := rc.Storage.LoadLogs(rc.Group)
	if err != nil {
		Error("Raft加载日志失败", "节点ID=", rc.ID, "组=", rc.Group, "错误=", err)
		return
	}
	rc.Log = logs
}

// AddLearner 添加一个Learner节点。
// nodeID: Learner节点ID
func (rc *RaftClient) AddLearner(nodeID string) {
	cmd := RaftCommand{
		Op:  addLearner,
		Key: nodeID,
	}
	rc.HandleClientRequest(cmd)
}

// AddLearnerWithSnapshot 添加一个通过快照快速同步的Learner节点。
// nodeID: Learner节点ID
// snapshotIndex: 快照对应的日志索引
// snapshotTerm: 快照对应的日志任期
func (rc *RaftClient) AddLearnerWithSnapshot(nodeID string, snapshotIndex, snapshotTerm int64) {
	cmd := RaftCommand{
		Op:            addLearnerWithSnapshot,
		Key:           nodeID,
		SnapshotIndex: snapshotIndex,
		SnapshotTerm:  snapshotTerm,
	}
	rc.HandleClientRequest(cmd)
}

// RemoveLearner 移除一个Learner节点。
// nodeID: Learner节点ID
func (rc *RaftClient) RemoveLearner(nodeID string) {
	cmd := RaftCommand{
		Op:  removeLearner,
		Key: nodeID,
	}
	rc.HandleClientRequest(cmd)
}

// IsLearner 检查当前节点是否为Learner
// 返回：是否为Learner
func (rc *RaftClient) IsLearner() bool {
	_, exists := rc.Learners.Load(rc.ID)
	return exists
}

// Close 关闭RaftClient，停止主循环
func (rc *RaftClient) Close() {
	rc.stopMainLoop()
}

// disableElectionTimer 禁用选举定时器（用于成为Leader后）
func (rc *RaftClient) disableElectionTimer() {
	rc.electionTimeout = math.MaxInt64
	Debug("Raft选举定时器已禁用", "节点ID=", rc.ID)
}
