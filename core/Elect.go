package core

import (
	"Majula/common"
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// ElectState 选举状态枚举
type ElectState int

const (
	ElectBusy    ElectState = iota // 忙碌状态
	ElectStandby                   // 待命状态
	ElectDuty                      // 值班状态
)

// 基础超时时间常量
const (
	BASE_OVERTIME_T = 3000 // 基础超时时间（毫秒）
)

// ElectConfig 选举配置
type ElectConfig struct {
	BaseOvertimeT int64  // 基础超时时间（毫秒）
	GroupName     string // 选举组名
}

// ElectCandidate 选举候选人
type ElectCandidate struct {
	ID            string     // 候选人ID（使用节点ID）
	State         ElectState // 当前状态
	BaseOvertimeT int64      // 基础超时时间
	GroupName     string     // 选举组名

	// 时间相关
	waitStandbyTime int64 // 待命等待时间
	waitDutyTime    int64 // 值班等待时间
	heartbeatTime   int64 // 下次心跳时间
	checkHealthTime int64 // 下次健康检查时间

	// 节点引用
	node *Node // 关联的 Majula 节点

	// 控制
	ctx     context.Context
	cancel  context.CancelFunc
	running int32 // 运行状态
	mu      sync.RWMutex

	// 统计
	heartbeatCount   int64 // 心跳计数
	stateChangeCount int64 // 状态变更计数
}

// NewElectCandidate 创建新的选举候选人
func NewElectCandidate(node *Node, config ElectConfig) *ElectCandidate {
	if config.BaseOvertimeT == 0 {
		config.BaseOvertimeT = BASE_OVERTIME_T
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &ElectCandidate{
		ID:            node.ID,
		State:         ElectBusy,
		BaseOvertimeT: config.BaseOvertimeT,
		GroupName:     config.GroupName,
		node:          node,
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start 启动选举候选人
func (ec *ElectCandidate) Start() error {
	if !atomic.CompareAndSwapInt32(&ec.running, 0, 1) {
		return fmt.Errorf("candidate already running")
	}

	Log("选举候选人启动", "候选人ID=", ec.ID)

	// 订阅心跳和放弃消息
	ec.subscribeToElectionTopics()

	// 启动主工作循环
	go ec.mainLoop()

	return nil
}

// Stop 停止选举候选人
func (ec *ElectCandidate) Stop() {
	if atomic.CompareAndSwapInt32(&ec.running, 1, 0) {
		Log("选举候选人停止", "候选人ID=", ec.ID)
		ec.cancel()
	}
}

// GetState 获取当前状态
func (ec *ElectCandidate) GetState() ElectState {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	return ec.State
}

// IsDuty 检查是否处于值班状态
func (ec *ElectCandidate) IsDuty() bool {
	return ec.GetState() == ElectDuty
}

// subscribeToElectionTopics 订阅选举相关主题
func (ec *ElectCandidate) subscribeToElectionTopics() {
	// 订阅心跳主题
	ec.node.addLocalSub(ec.getHeartbeatTopic(), ec.ID, func(topic string, from string, to string, content []byte) {
		ec.onHeartbeat(from, content)
	})

	// 订阅放弃主题
	ec.node.addLocalSub(ec.getGiveUpTopic(), ec.ID, func(topic string, from string, to string, content []byte) {
		ec.onGiveUp(from, content)
	})
}

// getHeartbeatTopic 获取心跳主题名
func (ec *ElectCandidate) getHeartbeatTopic() string {
	return fmt.Sprintf("election_heartbeat_%s", ec.GroupName)
}

// getGiveUpTopic 获取放弃主题名
func (ec *ElectCandidate) getGiveUpTopic() string {
	return fmt.Sprintf("election_giveup_%s", ec.GroupName)
}

// mainLoop 主工作循环
func (ec *ElectCandidate) mainLoop() {
	for {
		select {
		case <-ec.ctx.Done():
			return
		default:
			ec.processState()
			time.Sleep(100 * time.Millisecond) // 100ms 检查间隔
		}
	}
}

// processState 处理当前状态
func (ec *ElectCandidate) processState() {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	now := time.Now().UnixMilli()

	switch ec.State {
	case ElectBusy:
		ec.processBusyState(now)
	case ElectStandby:
		ec.processStandbyState(now)
	case ElectDuty:
		ec.processDutyState(now)
	}
}

// processBusyState 处理忙碌状态
func (ec *ElectCandidate) processBusyState(now int64) {
	if now >= ec.waitStandbyTime {
		Log("选举状态变更", "候选人ID=", ec.ID, "状态=忙碌->待命")
		ec.State = ElectStandby
		ec.stateChangeCount++

		// 计算待命等待时间
		randomOffset := rand.Int63n(ec.BaseOvertimeT)
		ec.waitDutyTime = now + ec.BaseOvertimeT + randomOffset
		ec.heartbeatTime = now + int64(ec.BaseOvertimeT/5)                                      // 心跳间隔为BaseOvertimeT/5
		ec.checkHealthTime = now + ec.BaseOvertimeT*3 + int64(rand.Intn(int(ec.BaseOvertimeT))) // 健康检查时间
	}
}

// processStandbyState 处理待命状态
func (ec *ElectCandidate) processStandbyState(now int64) {
	if now >= ec.waitDutyTime {
		Log("选举状态变更", "候选人ID=", ec.ID, "状态=待命->值班")
		ec.State = ElectDuty
		ec.stateChangeCount++

		// 成为值班者，开始发送心跳
		ec.heartbeatTime = now + int64(ec.BaseOvertimeT/5)
		ec.checkHealthTime = now + ec.BaseOvertimeT*3 + int64(rand.Intn(int(ec.BaseOvertimeT)))
		ec.sendHeartbeat()
	}
}

// processDutyState 处理值班状态
func (ec *ElectCandidate) processDutyState(now int64) {
	// 发送心跳
	if now >= ec.heartbeatTime {
		ec.sendHeartbeat()
		ec.heartbeatTime = now + int64(ec.BaseOvertimeT/5)
	}

	// 健康检查
	if now >= ec.checkHealthTime {
		ec.checkHealth()
		ec.checkHealthTime = now + ec.BaseOvertimeT*3 + int64(rand.Intn(int(ec.BaseOvertimeT)))
	}
}

// sendHeartbeat 发送心跳
func (ec *ElectCandidate) sendHeartbeat() {
	heartbeat := map[string]interface{}{
		"id":        ec.ID,
		"timestamp": time.Now().UnixMilli(),
		"group":     ec.GroupName,
	}

	data, _ := common.MarshalAny(heartbeat)

	// 通过 Majula 的 Pub/Sub 系统广播心跳
	ec.node.publishOnTopic(ec.getHeartbeatTopic(), string(data))

	ec.heartbeatCount++
	Debug("发送心跳", "候选人ID=", ec.ID)
}

// onHeartbeat 处理收到的心跳
func (ec *ElectCandidate) onHeartbeat(from string, content []byte) {
	if from == ec.ID {
		return // 忽略自己的心跳
	}

	var heartbeat map[string]interface{}
	if err := common.UnmarshalAny(content, &heartbeat); err != nil {
		Error("心跳消息反序列化失败", "候选人ID=", ec.ID, "错误=", err)
		return
	}

	// 检查是否是同一组的心跳
	if group, ok := heartbeat["group"].(string); !ok || group != ec.GroupName {
		return
	}

	ec.mu.Lock()
	defer ec.mu.Unlock()

	// 如果当前是值班者，收到其他心跳说明有冲突
	if ec.State == ElectDuty {
		Log("检测到冲突，放弃值班", "候选人ID=", ec.ID, "冲突方=", from)
		ec.State = ElectStandby
		ec.stateChangeCount++

		// 重新计算等待时间
		now := time.Now().UnixMilli()
		randomOffset := rand.Int63n(ec.BaseOvertimeT)
		ec.waitDutyTime = now + ec.BaseOvertimeT + randomOffset
	} else if ec.State == ElectStandby {
		// 如果是待命状态，延长等待时间
		now := time.Now().UnixMilli()
		randomOffset := rand.Int63n(ec.BaseOvertimeT)
		ec.waitDutyTime = now + ec.BaseOvertimeT + randomOffset
		Debug("因心跳延长等待时间", "候选人ID=", ec.ID, "来源=", from)
	}
}

// onGiveUp 处理放弃消息
func (ec *ElectCandidate) onGiveUp(from string, content []byte) {
	if from == ec.ID {
		return // 忽略自己的放弃消息
	}

	var giveUp map[string]interface{}
	if err := common.UnmarshalAny(content, &giveUp); err != nil {
		Error("放弃消息反序列化失败", "候选人ID=", ec.ID, "错误=", err)
		return
	}

	// 检查是否是同一组的放弃消息
	if group, ok := giveUp["group"].(string); !ok || group != ec.GroupName {
		return
	}

	ec.mu.Lock()
	defer ec.mu.Unlock()

	// 如果当前是待命状态，可以更快进入值班状态 - 按照参考程序的方式
	if ec.State == ElectStandby {
		now := time.Now().UnixMilli()
		// 减少等待时间
		ec.waitDutyTime = now + ec.BaseOvertimeT/2
		Debug("因放弃消息减少等待时间", "候选人ID=", ec.ID, "来源=", from)
	}
}

// checkHealth 健康检查
func (ec *ElectCandidate) checkHealth() {
	Debug("健康检查通过", "候选人ID=", ec.ID)
}

// GiveUp 主动放弃值班
func (ec *ElectCandidate) GiveUp() {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	if ec.State == ElectDuty {
		Log("主动放弃值班", "候选人ID=", ec.ID)
		ec.State = ElectStandby
		ec.stateChangeCount++

		giveUp := map[string]interface{}{
			"id":        ec.ID,
			"timestamp": time.Now().UnixMilli(),
			"group":     ec.GroupName,
		}

		data, _ := common.MarshalAny(giveUp)
		ec.node.publishOnTopic(ec.getGiveUpTopic(), string(data))

		now := time.Now().UnixMilli()
		randomOffset := rand.Int63n(ec.BaseOvertimeT)
		ec.waitDutyTime = now + ec.BaseOvertimeT + randomOffset
	}
}

// GetStats 获取统计信息
func (ec *ElectCandidate) GetStats() map[string]interface{} {
	ec.mu.RLock()
	defer ec.mu.RUnlock()

	return map[string]interface{}{
		"id":              ec.ID,
		"state":           ec.State,
		"group":           ec.GroupName,
		"heartbeat_count": ec.heartbeatCount,
		"state_changes":   ec.stateChangeCount,
		"is_duty":         ec.State == ElectDuty,
	}
}

// ElectManager 选举管理器
type ElectManager struct {
	node       *Node
	candidates map[string]*ElectCandidate
	mu         sync.RWMutex
}

// NewElectManager 创建选举管理器
func NewElectManager(node *Node) *ElectManager {
	return &ElectManager{
		node:       node,
		candidates: make(map[string]*ElectCandidate),
	}
}

// CreateCandidate 创建选举候选人
func (em *ElectManager) CreateCandidate(groupName string, config ElectConfig) (*ElectCandidate, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	if _, exists := em.candidates[groupName]; exists {
		return nil, fmt.Errorf("candidate for group %s already exists", groupName)
	}

	config.GroupName = groupName
	candidate := NewElectCandidate(em.node, config)
	em.candidates[groupName] = candidate

	return candidate, nil
}

// GetCandidate 获取选举候选人
func (em *ElectManager) GetCandidate(groupName string) (*ElectCandidate, bool) {
	em.mu.RLock()
	defer em.mu.RUnlock()

	candidate, exists := em.candidates[groupName]
	return candidate, exists
}

// RemoveCandidate 移除选举候选人
func (em *ElectManager) RemoveCandidate(groupName string) error {
	em.mu.Lock()
	defer em.mu.Unlock()

	candidate, exists := em.candidates[groupName]
	if !exists {
		return fmt.Errorf("candidate for group %s not found", groupName)
	}

	candidate.Stop()
	delete(em.candidates, groupName)

	return nil
}

// GetAllCandidates 获取所有候选人
func (em *ElectManager) GetAllCandidates() map[string]*ElectCandidate {
	em.mu.RLock()
	defer em.mu.RUnlock()

	result := make(map[string]*ElectCandidate)
	for k, v := range em.candidates {
		result[k] = v
	}

	return result
}

// GetStats 获取所有候选人的统计信息
func (em *ElectManager) GetStats() map[string]interface{} {
	em.mu.RLock()
	defer em.mu.RUnlock()

	stats := make(map[string]interface{})
	for groupName, candidate := range em.candidates {
		stats[groupName] = candidate.GetStats()
	}

	return stats
}
