package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
)

// Storage 封装了基于LevelDB的Raft持久化存储，支持日志、元数据、状态机KV的读写。
type Storage struct {
	db     *leveldb.DB // LevelDB数据库实例
	NodeId string      // 当前节点ID，用于隔离多节点数据
}

// NewStorage 创建一个新的Storage实例，打开指定路径的LevelDB。
// path: LevelDB文件路径
// nodeId: 节点ID（用于key前缀隔离）
// 返回：*Storage, error
func NewStorage(path, nodeId string) (*Storage, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return &Storage{db: db, NodeId: nodeId}, nil
}

// Close 关闭底层LevelDB数据库。
func (s *Storage) Close() error {
	if s.db == nil {
		return nil // 内存模式，无需关闭
	}
	return s.db.Close()
}

// makeKey 统一生成带 NodeId 和 Group 前缀的 Key。
// 格式: node:<nodeId>:group:<group>:<category>:<key>
func (s *Storage) makeKey(group, category, key string) []byte {
	return []byte(fmt.Sprintf("node:%s:group:%s:%s:%s", s.NodeId, group, category, key))
}

// -------------------- 日志存储 --------------------

// SaveLog 保存单条日志到LevelDB。
// group: 组ID
// entry: 日志条目
// 返回：error
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

// GetLog 获取指定索引的日志。
// group: 组ID
// index: 日志索引
// 返回：*RaftLogEntry, error
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

// DeleteLogsBefore 删除index之前的所有日志。
// group: 组ID
// index: 截止索引（不含）
// 返回：error
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

// LoadLogs 加载指定group的所有日志，按index升序排列。
// group: 组ID
// 返回：[]RaftLogEntry, error
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

// SaveMeta 持久化元数据（term、votedFor、commitIndex、lastApplied）。
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

// LoadMeta 加载group的元数据。
// 返回：term, votedFor, commitIndex, lastApplied, error
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

// PutState 存储状态机KV。
func (s *Storage) PutState(group, key string, value interface{}) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	data, _ := json.Marshal(value)
	return s.db.Put(s.makeKey(group, "state", key), data, nil)
}

// DeleteState 删除状态机KV。
func (s *Storage) DeleteState(group, key string) error {
	if s.db == nil {
		return nil // 内存模式，不持久化
	}
	return s.db.Delete(s.makeKey(group, "state", key), nil)
}

// GetState 获取状态机KV。
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

// DeleteLogsFrom 删除指定index及之后的所有日志。
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
