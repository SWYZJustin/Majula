package main

import "fmt"

// =====================
// Message
// =====================

type MessageType int

const (
	HeartBeat MessageType = iota
	CostRequest
	CostAck
	Quit
	Hello
	TcpRegister
	Other
	TopicInit
	TopicExit
	TopicPublish
	TopicSubscribeFlood
	RpcRequest
	RpcResponse
	RpcServiceFlood
	P2PMessage
)

func checkBroadCast(pType MessageType) bool {
	if pType == HeartBeat || pType == Hello || pType == TopicInit || pType == TopicExit || pType == TopicPublish || pType == TopicSubscribeFlood || pType == RpcServiceFlood {
		return true
	}
	return false
}

type MessageData struct {
	Type     MessageType
	Data     string
	BundleTo []string
	Entity   string
}
type Message struct {
	MessageData
	From       string
	To         string
	Route      string
	TTL        int16
	Lost       bool
	VersionSeq uint64
	LastSender string
	InvokeId   int64
}

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

func (m *Message) isImportant() bool {
	if m.Type == Other {
		return false
	}
	return true
}
