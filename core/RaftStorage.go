package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
)

// Storage 封装了基于LevelDB的Raft持久化存储，支持日志、元数据、状态机键值对的读写。
// Storage 存储结构体，包含LevelDB数据库实例和节点ID
type Storage struct {
	db     *leveldb.DB // LevelDB数据库实例
	NodeId string      // 当前节点ID，用于隔离多节点数据
}

// NewStorage 创建一个新的Storage实例，打开指定路径的LevelDB。
// 参数：path - LevelDB文件路径，nodeId - 节点ID（用于键前缀隔离）
// 返回：Storage指针和错误信息
func NewStorage(path, nodeId string) (*Storage, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return &Storage{db: db, NodeId: nodeId}, nil
}

// Close 关闭底层LevelDB数据库
// 返回：错误信息
func (s *Storage) Close() error {
	if s.db == nil {
		return nil // 内存模式，无需关闭
	}
	return s.db.Close()
}

// makeKey 统一生成带节点ID和组前缀的键
// 格式：node:<节点ID>:group:<组名>:<类别>:<键名>
// 参数：group - 组名，category - 类别，key - 键名
// 返回：字节数组形式的键
func (s *Storage) makeKey(group, category, key string) []byte {
	return []byte(fmt.Sprintf("node:%s:group:%s:%s:%s", s.NodeId, group, category, key))
}

// -------------------- 日志存储 --------------------

// SaveLog 保存单条日志到LevelDB
// 参数：group - 组ID，entry - 日志条目
// 返回：错误信息
func (s *Storage) SaveLog(group string, entry RaftLogEntry) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	key := fmt.Sprintf("%020d", entry.Index)
	fullKey := s.makeKey(group, "log", key)
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return s.db.Put(fullKey, data, nil)
}

// GetLog 获取指定索引的日志
// 参数：group - 组ID，index - 日志索引
// 返回：RaftLogEntry指针和错误信息
func (s *Storage) GetLog(group string, index int64) (*RaftLogEntry, error) {
	if s.db == nil {
		return nil, fmt.Errorf("no storage available")
	}
	key := fmt.Sprintf("%020d", index)
	fullKey := s.makeKey(group, "log", key)
	data, err := s.db.Get(fullKey, nil)
	if err != nil {
		return nil, err
	}
	var entry RaftLogEntry
	err = json.Unmarshal(data, &entry)
	return &entry, err
}

// DeleteLogsBefore 删除指定索引之前的所有日志
// 参数：group - 组ID，index - 截止索引（不含）
// 返回：错误信息
func (s *Storage) DeleteLogsBefore(group string, index int64) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	prefix := fmt.Sprintf("node:%s:group:%s:log:", s.NodeId, group)
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		k := string(iter.Key())
		if strings.HasPrefix(k, prefix) {
			logIdx, _ := strconv.ParseInt(strings.TrimPrefix(k, prefix), 10, 64)
			if logIdx < index {
				_ = s.db.Delete(iter.Key(), nil)
			}
		}
	}
	return nil
}

// LoadLogs 加载指定组的所有日志，按索引升序排列
// 参数：group - 组ID
// 返回：RaftLogEntry切片和错误信息
func (s *Storage) LoadLogs(group string) ([]RaftLogEntry, error) {
	if s.db == nil {
		return []RaftLogEntry{}, nil // 内存模式，返回空日志
	}
	prefix := fmt.Sprintf("node:%s:group:%s:log:", s.NodeId, group)
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()
	var logs []RaftLogEntry
	for iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, prefix) {
			var entry RaftLogEntry
			_ = json.Unmarshal(iter.Value(), &entry)
			logs = append(logs, entry)
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].Index < logs[j].Index })
	return logs, nil
}

// -------------------- Meta 信息 --------------------

// SaveMeta 持久化元数据（任期、投票对象、提交索引、最后应用索引）
// 参数：group - 组ID，term - 任期，votedFor - 投票对象，commitIndex - 提交索引，lastApplied - 最后应用索引
// 返回：错误信息
func (s *Storage) SaveMeta(group string, term int64, votedFor string, commitIndex, lastApplied int64) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	meta := map[string]interface{}{
		"term":        term,
		"votedFor":    votedFor,
		"commitIndex": commitIndex,
		"lastApplied": lastApplied,
	}
	data, _ := json.Marshal(meta)
	return s.db.Put(s.makeKey(group, "meta", "info"), data, nil)
}

// LoadMeta 加载组的元数据
// 参数：group - 组ID
// 返回：任期、投票对象、提交索引、最后应用索引和错误信息
func (s *Storage) LoadMeta(group string) (int64, string, int64, int64, error) {
	if s.db == nil {
		return 0, "", 0, 0, nil // 内存模式，返回默认值
	}
	data, err := s.db.Get(s.makeKey(group, "meta", "info"), nil)
	if err != nil {
		return 0, "", 0, 0, err
	}
	var meta map[string]interface{}
	_ = json.Unmarshal(data, &meta)
	lastApplied := int64(0)
	if v, ok := meta["lastApplied"]; ok {
		lastApplied = int64(v.(float64))
	}
	return int64(meta["term"].(float64)), meta["votedFor"].(string), int64(meta["commitIndex"].(float64)), lastApplied, nil
}

// -------------------- 状态机 Key-Value --------------------

// PutState 存储状态机键值对
// 参数：group - 组ID，key - 键名，value - 值
// 返回：错误信息
func (s *Storage) PutState(group, key string, value interface{}) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	data, _ := json.Marshal(value)
	return s.db.Put(s.makeKey(group, "state", key), data, nil)
}

// DeleteState 删除状态机键值对
// 参数：group - 组ID，key - 键名
// 返回：错误信息
func (s *Storage) DeleteState(group, key string) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	return s.db.Delete(s.makeKey(group, "state", key), nil)
}

// GetState 获取状态机键值对
// 参数：group - 组ID，key - 键名
// 返回：值和错误信息
func (s *Storage) GetState(group, key string) (interface{}, error) {
	if s.db == nil {
		return nil, fmt.Errorf("no storage available")
	}
	data, err := s.db.Get(s.makeKey(group, "state", key), nil)
	if err != nil {
		return nil, err
	}
	var value interface{}
	err = json.Unmarshal(data, &value)
	return value, err
}

// DeleteLogsFrom 删除指定索引及之后的所有日志
// 参数：group - 组ID，startIndex - 起始索引
// 返回：错误信息
func (s *Storage) DeleteLogsFrom(group string, startIndex int64) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	prefix := fmt.Sprintf("node:%s:group:%s:log:", s.NodeId, group)
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, prefix) {
			logIdx, _ := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64)
			if logIdx >= startIndex {
				_ = s.db.Delete(iter.Key(), nil)
			}
		}
	}
	return nil
}

// -------------------- 状态机状态传输 --------------------

// GetFullState 获取状态机的完整状态，用于Learner快速同步
// 参数：group - 组ID
// 返回：完整状态机状态和错误信息
func (s *Storage) GetFullState(group string) (map[string]interface{}, error) {
	if s.db == nil {
		return nil, fmt.Errorf("no storage available")
	}

	state := make(map[string]interface{})
	prefix := fmt.Sprintf("node:%s:group:%s:state:", s.NodeId, group)
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, prefix) {
			// 提取键名（去掉前缀）
			stateKey := strings.TrimPrefix(key, prefix)
			// 解析值
			var value interface{}
			if err := json.Unmarshal(iter.Value(), &value); err != nil {
				continue // 跳过无法解析的值
			}
			state[stateKey] = value
		}
	}

	return state, nil
}

// SetFullState 设置状态机的完整状态，用于Learner快速同步
// 参数：group - 组ID，state - 完整状态机状态
// 返回：错误信息
func (s *Storage) SetFullState(group string, state map[string]interface{}) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}

	// 先清除现有状态
	s.clearState(group)

	// 设置新状态
	for key, value := range state {
		if err := s.PutState(group, key, value); err != nil {
			return err
		}
	}

	return nil
}

// clearState 清除指定组的所有状态机数据
// 参数：group - 组ID
// 返回：错误信息
func (s *Storage) clearState(group string) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}

	prefix := fmt.Sprintf("node:%s:group:%s:state:", s.NodeId, group)
	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if strings.HasPrefix(key, prefix) {
			_ = s.db.Delete(iter.Key(), nil)
		}
	}

	return nil
}

// GetStateSnapshot 获取状态机快照，包含元数据
// 参数：group - 组ID
// 返回：状态机快照和错误信息
func (s *Storage) GetStateSnapshot(group string) (*StateSnapshot, error) {
	if s.db == nil {
		return nil, fmt.Errorf("no storage available")
	}

	// 获取当前元数据
	term, _, commitIndex, lastApplied, err := s.LoadMeta(group)
	if err != nil {
		return nil, err
	}

	// 获取完整状态
	state, err := s.GetFullState(group)
	if err != nil {
		return nil, err
	}

	snapshot := &StateSnapshot{
		Group:       group,
		Term:        term,
		CommitIndex: commitIndex,
		LastApplied: lastApplied,
		State:       state,
		Timestamp:   time.Now(),
	}

	return snapshot, nil
}

// StateSnapshot 状态机快照结构体
type StateSnapshot struct {
	Group       string                 `json:"group"`        // 组名
	Term        int64                  `json:"term"`         // 当前任期
	CommitIndex int64                  `json:"commit_index"` // 提交索引
	LastApplied int64                  `json:"last_applied"` // 最后应用索引
	State       map[string]interface{} `json:"state"`        // 状态机状态
	Timestamp   time.Time              `json:"timestamp"`    // 快照时间戳
}
