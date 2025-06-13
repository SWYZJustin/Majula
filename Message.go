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
)

func checkBroadCast(pType MessageType) bool {
	if pType == HeartBeat || pType == Hello || pType == TopicInit || pType == TopicExit || pType == TopicPublish || pType == TopicSubscribeFlood {
		return true
	}
	return false
}

type MessageData struct {
	Type     MessageType
	Data     string
	BundleTo []string
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
}

func (m *Message) Print() string {
	var typeStr string
	switch m.Type {
	case HeartBeat:
		typeStr = "HeartBeat"
	case CostRequest:
		typeStr = "CostRequest"
	case CostAck:
		typeStr = "CostAck"
	case Quit:
		typeStr = "Quit"
	case Hello:
		typeStr = "Hello"
	case TcpRegister:
		typeStr = "TcpRegister"
	default:
		typeStr = "Unknown"
	}

	return fmt.Sprintf(
		"\n[Message]\n"+
			"  Type       : %s\n"+
			"  Data       : %s\n"+
			"  From       : %s\n"+
			"  To         : %s\n"+
			"  Route      : %s\n"+
			"  TTL        : %d\n"+
			"  Lost       : %v\n"+
			"  VersionSeq : %d\n",
		typeStr,
		m.Data,
		m.From,
		m.To,
		m.Route,
		m.TTL,
		m.Lost,
		m.VersionSeq,
	)
}

func (m *Message) isImportant() bool {
	if m.Type == Other {
		return false
	}
	return true
}
