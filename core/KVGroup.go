package core

import "sync"

type KVGroup struct {
	mu sync.RWMutex
	kv *OrderedKV
}

func NewKVGroup() *KVGroup {
	return &KVGroup{
		kv: NewOrderedKV(),
	}
}

// Put 插入或更新键值对，并追加日志
func (g *KVGroup) Put(key string, value interface{}, term, index int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kv.Put(key, value, term, index)
}

// Delete 删除键
func (g *KVGroup) Delete(key string, term, index int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kv.Delete(key, term, index)
}

// Get 查询键值
func (g *KVGroup) Get(key string) (interface{}, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.kv.Get(key)
}

// Range 遍历所有键值
func (g *KVGroup) Range(f func(key string, value interface{})) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	g.kv.Range(f)
}

// AppendLog 追加日志
func (g *KVGroup) AppendLog(entry LogEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kv.AppendLog(entry)
}

// ReplayLog 重放日志
func (g *KVGroup) ReplayLog() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kv.ReplayLog()
}

// KVEntry 表示有序KV结构中的一个键值对。
type KVEntry struct {
	Key   string
	Value interface{}
}

// LogEntry 表示一条日志操作记录，用于一致性协议的日志复制。
type LogEntry struct {
	Index int64
	Term  int64
	Op    string // "put" or "delete"
	Key   string
	Value interface{}
}

// OrderedKV 实现有序KV存储和日志记录，支持顺序遍历和高效查找。
type OrderedKV struct {
	kvList []KVEntry      // 有序KV
	kvMap  map[string]int // key -> kvList下标
	log    []LogEntry     // 日志
}

// NewOrderedKV 创建一个新的OrderedKV实例。
func NewOrderedKV() *OrderedKV {
	return &OrderedKV{
		kvList: make([]KVEntry, 0),
		kvMap:  make(map[string]int),
		log:    make([]LogEntry, 0),
	}
}

// Put 向有序KV中插入或更新一个键值对，并追加日志。
// key: 键，value: 值，term: 任期，index: 日志序号
func (o *OrderedKV) Put(key string, value interface{}, term, index int64) {
	if idx, ok := o.kvMap[key]; ok {
		o.kvList[idx].Value = value
	} else {
		o.kvList = append(o.kvList, KVEntry{Key: key, Value: value})
		o.kvMap[key] = len(o.kvList) - 1
	}
	o.log = append(o.log, LogEntry{Index: index, Term: term, Op: "put", Key: key, Value: value})
}

// Delete 从有序KV中删除一个键，并追加日志。
// key: 键，term: 任期，index: 日志序号
func (o *OrderedKV) Delete(key string, term, index int64) {
	if idx, ok := o.kvMap[key]; ok {
		o.kvList = append(o.kvList[:idx], o.kvList[idx+1:]...)
		delete(o.kvMap, key)
		for i := idx; i < len(o.kvList); i++ {
			o.kvMap[o.kvList[i].Key] = i
		}
	}
	o.log = append(o.log, LogEntry{Index: index, Term: term, Op: "delete", Key: key})
}

// Get 查询指定key的值。
// 返回值：value, 是否存在
func (o *OrderedKV) Get(key string) (interface{}, bool) {
	if idx, ok := o.kvMap[key]; ok {
		return o.kvList[idx].Value, true
	}
	return nil, false
}

// Range 顺序遍历所有键值对，回调函数f(key, value)
func (o *OrderedKV) Range(f func(key string, value interface{})) {
	for _, entry := range o.kvList {
		f(entry.Key, entry.Value)
	}
}

// AppendLog 追加一条日志记录。
func (o *OrderedKV) AppendLog(entry LogEntry) {
	o.log = append(o.log, entry)
}

// ReplayLog 根据日志重放恢复有序KV状态。
func (o *OrderedKV) ReplayLog() {
	o.kvList = make([]KVEntry, 0)
	o.kvMap = make(map[string]int)
	for _, entry := range o.log {
		if entry.Op == "put" {
			o.Put(entry.Key, entry.Value, entry.Term, entry.Index)
		} else if entry.Op == "delete" {
			o.Delete(entry.Key, entry.Term, entry.Index)
		}
	}
}
