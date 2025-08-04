package core

import "fmt"

// =====================
// 消息
// =====================

// MessageType 消息类型枚举
type MessageType int

const (
	HeartBeat               MessageType = iota // 心跳消息
	CostRequest                                // 代价请求
	CostAck                                    // 代价确认
	Quit                                       // 退出消息
	Hello                                      // 问候消息
	TcpRegister                                // TCP注册
	Other                                      // 其他消息
	TopicInit                                  // 主题初始化
	TopicExit                                  // 主题退出
	TopicPublish                               // 主题发布
	TopicSubscribeFlood                        // 主题订阅洪泛
	RpcRequest                                 // RPC请求
	RpcResponse                                // RPC响应
	RpcServiceFlood                            // RPC服务洪泛
	P2PMessage                                 // P2P消息
	FRPData                                    // FRP数据
	FRPAck                                     // FRP确认
	FRPClose                                   // FRP关闭
	FRPResendRequest                           // FRP重发请求
	RaftTopicInit                              // Raft主题初始化
	RaftTopicExit                              // Raft主题退出
	RaftTopicPublish                           // Raft主题发布
	RaftTopicSubscribeFlood                    // Raft主题订阅洪泛
	RaftMessage                                // Raft消息
	RaftLearnerJoin                            // Raft学习者加入
)

// checkBroadCast 检查消息类型是否为广播消息
func checkBroadCast(pType MessageType) bool {
	if pType == HeartBeat || pType == Hello || pType == TopicInit || pType == TopicExit || pType == TopicPublish || pType == TopicSubscribeFlood || pType == RpcServiceFlood {
		return true
	}
	return false
}

// MessageData 消息数据结构，包含消息类型、数据内容、目标列表和实体信息
type MessageData struct {
	Type     MessageType // 消息类型
	Data     string      // 消息数据内容
	BundleTo []string    // 目标节点列表
	Entity   string      // 实体标识
}

// Message 完整的消息结构，继承MessageData并添加路由和传输相关信息
type Message struct {
	MessageData
	From       string // 发送方节点ID
	To         string // 接收方节点ID
	Route      string // 路由路径
	TTL        int16  // 生存时间
	Lost       bool   // 是否丢失
	VersionSeq uint64 // 版本序列号
	LastSender string // 最后发送方
	InvokeId   int64  // 调用ID
}

// Print 将消息格式化为字符串输出，用于调试和日志记录
func (m *Message) Print() string {
	return fmt.Sprintf(
		`[Message]
  Type       : %v
  Data       : %s
  Entity     : %s
  BundleTo   : %v
  From       : %s
  To         : %s
  Route      : %s
  TTL        : %d
  Lost       : %v
  VersionSeq : %d
  LastSender : %s
  InvokeId   : %d
`,
		m.Type,
		m.Data,
		m.Entity,
		m.BundleTo,
		m.From,
		m.To,
		m.Route,
		m.TTL,
		m.Lost,
		m.VersionSeq,
		m.LastSender,
		m.InvokeId,
	)
}

// isImportant 判断消息是否为重要消息（非Other类型）
func (m *Message) isImportant() bool {
	if m.Type == Other {
		return false
	}
	return true
}

// isFrp 判断消息是否为FRP相关消息
func (m *Message) isFrp() bool {
	switch m.Type {
	case FRPData, FRPAck, FRPClose, FRPResendRequest:
		return true
	default:
		return false
	}
}
