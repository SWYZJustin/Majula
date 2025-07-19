package core

// =====================
// Channel User & Worker
// =====================

// The actual worker that deal with the message

type ChannelWorker interface {
	getID() string
	sendTo(peerId string, msg *Message)
	broadCast(msg *Message)
	Close() error
}
