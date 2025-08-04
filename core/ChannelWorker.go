package core

// =====================
// 通道用户和工作者
// =====================

// 实际处理消息的工作者

type ChannelWorker interface {
	getID() string
	sendTo(peerId string, msg *Message)
	broadCast(msg *Message)
	Close() error
}
