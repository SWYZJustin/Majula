package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

func (s *StreamStub) DebugPrint(name string, message string) {
	return
	fmt.Printf("[%s: %s] %s\n", s.myId, name, message)
}

type WindowBuffer struct {
	mu         sync.Mutex
	startSeq   int64
	size       int
	data       [][]byte
	filled     []bool
	retryCount []int
	sendTime   []time.Time
}

func NewWindowBuffer(startSeq int64, size int) *WindowBuffer {
	return &WindowBuffer{
		startSeq:   startSeq,
		size:       size,
		data:       make([][]byte, size),
		filled:     make([]bool, size),
		retryCount: make([]int, size),
		sendTime:   make([]time.Time, size),
		mu:         sync.Mutex{},
	}
}

func (wb *WindowBuffer) Put(seq int64, value []byte) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if seq < wb.startSeq || seq >= wb.startSeq+int64(wb.size) {
		return false
	}
	index := int(seq % int64(wb.size))
	wb.data[index] = value
	wb.filled[index] = true
	wb.retryCount[index] = 0
	wb.sendTime[index] = time.Now()
	return true
}

func (wb *WindowBuffer) Get(seq int64) ([]byte, bool) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if seq < wb.startSeq || seq >= wb.startSeq+int64(wb.size) {
		return nil, false
	}
	index := int(seq % int64(wb.size))
	if !wb.filled[index] {
		return nil, false
	}
	return wb.data[index], true
}

func (wb *WindowBuffer) Advance() bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	index := int((wb.startSeq) % int64(wb.size))
	if !wb.filled[index] {
		return false
	}
	wb.data[index] = nil
	wb.filled[index] = false
	wb.startSeq++
	return true
}

func (wb *WindowBuffer) BundleAdvanceUpTo(maxSeq int64) int {
	wb.mu.Lock()
	defer wb.mu.Unlock()

	count := 0
	for wb.startSeq <= maxSeq {
		index := int((wb.startSeq) % int64(wb.size))
		if !wb.filled[index] {
			break
		}
		wb.data[index] = nil
		wb.filled[index] = false
		wb.retryCount[index] = 0
		wb.sendTime[index] = time.Time{}
		wb.startSeq++
		count++
	}
	return count
}

func (wb *WindowBuffer) IsFilled(seq int64) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	offset := seq - wb.startSeq
	if offset < 0 || offset >= int64(wb.size) {
		return false
	}
	index := int(seq % int64(wb.size))
	return wb.filled[index]
}

func (wb *WindowBuffer) GetWithMeta(seq int64) ([]byte, int, time.Time, bool) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if seq < wb.startSeq || seq >= wb.startSeq+int64(wb.size) {
		return nil, 0, time.Time{}, false
	}
	index := int(seq % int64(wb.size))
	if !wb.filled[index] {
		return nil, 0, time.Time{}, false
	}
	return wb.data[index], wb.retryCount[index], wb.sendTime[index], true
}

func (wb *WindowBuffer) IncrementRetry(seq int64) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	if seq < wb.startSeq || seq >= wb.startSeq+int64(wb.size) {
		return
	}
	index := int(seq % int64(wb.size))
	wb.retryCount[index]++
	wb.sendTime[index] = time.Now()
}

func (wb *WindowBuffer) RemoveUpTo(seq int64) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	for s := wb.startSeq; s <= seq; s++ {
		index := int(s % int64(wb.size))
		wb.data[index] = nil
		wb.filled[index] = false
		wb.retryCount[index] = 0
		wb.sendTime[index] = time.Time{}
	}
	wb.startSeq = seq + 1
}

func (wb *WindowBuffer) IsFull() bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return wb.Count() >= wb.size
}

func (wb *WindowBuffer) Count() int {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	count := 0
	for _, filled := range wb.filled {
		if filled {
			count++
		}
	}
	return count
}

func (wb *WindowBuffer) CanPut(seq int64) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return seq >= wb.startSeq && seq < wb.startSeq+int64(wb.size)
}

const (
	MAX_SEND_WINDOW_SIZE = 7024
	MAX_RETRY_COUNT      = 5
	ACK_TIMEOUT          = 5 * time.Second
)

const RECV_ACK_THRESHOLD = 128
const RECV_ACK_TIMEOUT = 200 * time.Millisecond
const SEND_RESEND_TIMEOUT = 200 * time.Millisecond
const maxResendPerCall = 100

type FRPDataPayload struct {
	TargetStubID string `json:"stub_id"`
	Seq          int64  `json:"seq"`
	Data         []byte `json:"data"`
}

func (p FRPDataPayload) String() string {
	return fmt.Sprintf("FRPDataPayload{TargetStubID: %s, Seq: %d, Data: %q}", p.TargetStubID, p.Seq, p.Data)
}

type FRPAckPayload struct {
	TargetStubID string `json:"stub_id"`
	Ack          int64  `json:"ack"`
}

type FRPResendRequestPayload struct {
	TargetStubID string `json:"stub_id"`
	Seq          int64  `json:"seq"`
}

type FRPCloseAckPayload struct {
	TargetStubID string `json:"stub_id"`
}

func (p FRPAckPayload) String() string {
	return fmt.Sprintf("FRPAckPayload{TargetStubID: %s, Ack: %d}", p.TargetStubID, p.Ack)
}

func (p FRPResendRequestPayload) String() string {
	return fmt.Sprintf("FRPResendRequestPayload{TargetStubID: %s, Seq: %d}", p.TargetStubID, p.Seq)
}

func (p FRPCloseAckPayload) String() string {
	return fmt.Sprintf("FRPCloseAckPayload{TargetStubID: %s}", p.TargetStubID)
}

type frpMessage struct {
	msgType MessageType
	payload []byte
}

type StreamStub struct {
	conn       net.Conn
	node       *Node
	myId       string
	peerNodeId string
	peerStubId string
	myNodeId   string

	sendSeq        int64
	lastAckedSeq   int64
	recvWindow     *WindowBuffer
	recvWindowLock sync.Mutex
	recvSeq        int64

	sendWindowLock sync.Mutex
	windowCond     *sync.Cond

	sendWindow *WindowBuffer

	cancelCtx context.Context
	cancel    context.CancelFunc
	desc      string

	ackMode          string
	recvSinceLastAck int64
	lastAckTime      time.Time

	resendRequestedAt map[int64]time.Time

	writeChan chan []byte

	lastActivityTime atomic.Value

	closeAcked chan struct{}

	fastConnect   bool
	delayedResend bool

	delayedResendRequest bool

	inChan chan frpMessage

	lazyCloseOnce sync.Once

	readChan chan []byte
}

func (stub *StreamStub) sendDataLoop(ctx context.Context) {
	stub.DebugPrint("startSendDataLoop", stub.myId)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("sendDataLoop cancelled")
			stub.doClose()
			return
		case data := <-stub.readChan:
			stub.lastActivityTime.Store(time.Now())
			seq := atomic.AddInt64(&stub.sendSeq, 1)

			stub.sendWindowLock.Lock()
			for !stub.sendWindow.CanPut(seq) {
				if seq < stub.sendWindow.startSeq {
					fmt.Printf("sendSeq %d is behind window start %d, aborting\n", seq, stub.sendWindow.startSeq)
					stub.sendWindowLock.Unlock()
					stub.doClose()
					return
				}
				stub.windowCond.Wait()
			}
			stub.sendWindow.Put(seq, data)
			stub.sendWindowLock.Unlock()

			stub.sendData(seq, data)
		}
	}
}

func (stub *StreamStub) sendData(seq int64, data []byte) {
	stub.lastActivityTime.Store(time.Now())
	payload := FRPDataPayload{
		TargetStubID: stub.peerStubId,
		Seq:          seq,
		Data:         data,
	}
	payloadJSON, _ := json.Marshal(payload)

	msg := &Message{
		MessageData: MessageData{
			Type: FRPData,
			Data: string(payloadJSON),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        100,
		LastSender: stub.myNodeId,
	}

	stub.DebugPrint("stub "+stub.myId+"sendData", msg.Print())

	go stub.node.sendTo(stub.peerNodeId, msg)
}

func (stub *StreamStub) onData(content []byte) {
	stub.lastActivityTime.Store(time.Now())
	var payload FRPDataPayload
	if err := json.Unmarshal(content, &payload); err != nil || payload.TargetStubID != stub.myId {
		stub.DebugPrint("stub "+stub.myId+"onData", "data error")
		return
	}
	stub.DebugPrint("stub "+stub.myId+"onData", payload.String())

	stub.recvWindowLock.Lock()
	defer stub.recvWindowLock.Unlock()

	if !stub.recvWindow.Put(payload.Seq, payload.Data) {
		return
	}

	progress := false
	startSeq := stub.recvSeq + 1
	for seq := startSeq; seq < startSeq+MAX_SEND_WINDOW_SIZE; seq++ {
		data, ok := stub.recvWindow.Get(seq)
		if !ok {
			break
		}
		stub.enqueueWrite(data)
		stub.recvSeq = seq
		progress = true
	}
	if progress {
		stub.recvWindow.BundleAdvanceUpTo(stub.recvSeq)
	}

	if !progress {
		sent := 0
		for seq := stub.recvSeq + 1; seq < payload.Seq && sent < maxResendPerCall; seq++ {
			if !stub.recvWindow.IsFilled(seq) {
				stub.sendResendRequest(seq)
				sent++
			}
		}
	}

	now := time.Now()
	switch stub.ackMode {
	case "immediate":
		stub.sendAck()
	case "delayed":
		stub.recvSinceLastAck++
		if stub.recvSinceLastAck >= RECV_ACK_THRESHOLD || now.Sub(stub.lastAckTime) >= RECV_ACK_TIMEOUT {
			stub.sendAck()
			stub.recvSinceLastAck = 0
			stub.lastAckTime = now
		}
	default:
		stub.sendAck()
	}
}

func (stub *StreamStub) sendAck() {
	stub.lastActivityTime.Store(time.Now())
	ackPayload := FRPAckPayload{
		TargetStubID: stub.peerStubId,
		Ack:          stub.recvSeq,
	}
	ackData, _ := json.Marshal(ackPayload)

	ackMsg := &Message{
		MessageData: MessageData{
			Type: FRPAck,
			Data: string(ackData),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        100,
		LastSender: stub.myNodeId,
	}
	stub.DebugPrint("stub "+stub.myId+"sendAck", ackMsg.Print())
	stub.node.sendTo(stub.peerNodeId, ackMsg)
}

func (stub *StreamStub) onAck(content []byte) {
	stub.lastActivityTime.Store(time.Now())
	var payload FRPAckPayload
	if err := json.Unmarshal(content, &payload); err != nil || payload.TargetStubID != stub.myId {
		stub.DebugPrint("stub "+stub.myId+"onAck", "data error")
		return
	}
	stub.DebugPrint("stub "+stub.myId+"onAck", payload.String())
	stub.sendWindowLock.Lock()
	defer stub.sendWindowLock.Unlock()

	stub.sendWindow.RemoveUpTo(payload.Ack)

	stub.lastAckedSeq = payload.Ack

	stub.windowCond.Broadcast()
}

func (stub *StreamStub) onClose() {
	stub.DebugPrint("stub "+stub.myId+"close", "")
	stub.lazyClose(time.Second * 5)
}

func (stub *StreamStub) doClose() {
	stub.lastActivityTime.Store(time.Now())
	msg := &Message{
		MessageData: MessageData{
			Type: FRPClose,
			Data: stub.myId,
		},
		From: stub.myNodeId,
		To:   stub.peerNodeId,
		TTL:  100,
	}
	stub.DebugPrint("stub "+stub.myId+"sendClose", msg.Print())
	stub.node.sendTo(stub.peerNodeId, msg)
	stub.lazyClose(time.Second * 5)
}

func (stub *StreamStub) retryLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stub.resendUnackedData()
		}
	}
}

func (stub *StreamStub) startRetryLoop() {
	go stub.retryLoop(stub.cancelCtx)
}

func (stub *StreamStub) resendUnackedData() {
	stub.sendWindowLock.Lock()
	defer stub.sendWindowLock.Unlock()
	for seq := stub.sendWindow.startSeq; seq < stub.sendSeq; seq++ {
		stub.lastActivityTime.Store(time.Now())
		data, retries, sentAt, ok := stub.sendWindow.GetWithMeta(seq)
		if !ok {
			continue
		}

		if stub.fastConnect {
			if retries >= MAX_RETRY_COUNT {
				fmt.Println("Too many retries, closing stream:", stub.myId)
				fmt.Println("conn close due to too many retries")
				stub.doClose()
				return
			}
		}

		if stub.delayedResend {
			if time.Since(sentAt) < ACK_TIMEOUT {
				continue
			}
		}
		stub.sendWindow.IncrementRetry(seq)
		stub.sendData(seq, data)
	}

}

func NewStreamStub(node *Node, conn net.Conn, myId, peerNodeId, peerStubId, myNodeId string) *StreamStub {
	ctx, cancel := context.WithCancel(context.Background())

	stub := &StreamStub{
		conn:       conn,
		node:       node,
		myId:       myId,
		peerNodeId: peerNodeId,
		peerStubId: peerStubId,
		myNodeId:   myNodeId,
		recvWindow: NewWindowBuffer(1, MAX_SEND_WINDOW_SIZE),
		sendWindow: NewWindowBuffer(1, MAX_SEND_WINDOW_SIZE),

		cancel:           cancel,
		recvSinceLastAck: 0,
		lastAckTime:      time.Now(),
		cancelCtx:        ctx,
		sendWindowLock:   sync.Mutex{},

		resendRequestedAt: make(map[int64]time.Time),
		writeChan:         make(chan []byte, 8192),

		fastConnect:          false,
		delayedResend:        false,
		delayedResendRequest: false,

		ackMode: "immediate",

		inChan:        make(chan frpMessage, 8192),
		lazyCloseOnce: sync.Once{},
		readChan:      make(chan []byte, 8192),
	}
	stub.lastActivityTime.Store(time.Now())
	stub.windowCond = sync.NewCond(&stub.sendWindowLock)

	go stub.frpMessageInLoop()

	return stub
}

func (stub *StreamStub) startReadFromConn() {
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := stub.conn.Read(buf)
			if err != nil {
				fmt.Println("conn read error:", err)
				stub.doClose()
				return
			}
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])

				select {
				case stub.readChan <- data:
				case <-stub.cancelCtx.Done():
					return
				}
			}
		}
	}()
}

func (stub *StreamStub) frpMessageInLoop() {
	for {
		select {
		case <-stub.cancelCtx.Done():
			return
		case msg := <-stub.inChan:
			switch msg.msgType {
			case FRPData:
				stub.onData(msg.payload)
			case FRPAck:
				stub.onAck(msg.payload)
			case FRPResendRequest:
				stub.onResendRequest(msg.payload)
			case FRPClose:
				stub.onClose()
			default:
				fmt.Println("Unknown FRP message type")
			}
		}
	}
}

func (stub *StreamStub) sendResendRequest(seq int64) {
	stub.lastActivityTime.Store(time.Now())
	stub.sendWindowLock.Lock()
	lastTime, requested := stub.resendRequestedAt[seq]
	if stub.delayedResendRequest {
		if requested && time.Since(lastTime) < SEND_RESEND_TIMEOUT {
			stub.sendWindowLock.Unlock()
			return
		}
	}
	stub.resendRequestedAt[seq] = time.Now()
	stub.sendWindowLock.Unlock()

	req := FRPResendRequestPayload{
		TargetStubID: stub.peerStubId,
		Seq:          seq,
	}
	data, _ := json.Marshal(req)

	msg := &Message{
		MessageData: MessageData{
			Type: FRPResendRequest,
			Data: string(data),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        100,
		LastSender: stub.myNodeId,
	}
	stub.DebugPrint("stub "+stub.myId+"sendResendRequest", msg.Print())
	stub.node.sendTo(stub.peerNodeId, msg)
}

func (stub *StreamStub) onResendRequest(content []byte) {
	stub.lastActivityTime.Store(time.Now())

	var payload FRPResendRequestPayload
	if err := json.Unmarshal(content, &payload); err != nil || payload.TargetStubID != stub.myId {
		stub.DebugPrint("stub "+stub.myId+"onResendRequest", "data error")
		return
	}
	stub.DebugPrint("stub "+stub.myId+"sendResendRequest", payload.String())
	stub.sendWindowLock.Lock()
	data, _, sentAt, ok := stub.sendWindow.GetWithMeta(payload.Seq)
	stub.sendWindowLock.Unlock()
	if stub.delayedResend {
		if stub.delayedResend {
			if time.Since(sentAt) < ACK_TIMEOUT {
				return
			}
		}
	}
	if ok {
		stub.sendData(payload.Seq, data)
	}
}

func (stub *StreamStub) enqueueWrite(data []byte) {
	select {
	case stub.writeChan <- data:
	default:
		fmt.Println("Write channel full, dropping data")
	}
}

func (stub *StreamStub) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-stub.writeChan:
			_, err := stub.conn.Write(data)
			if err != nil {
				fmt.Println("Write error:", err)
				stub.doClose()
				return
			} else {
				stub.lastActivityTime.Store(time.Now())
			}
		}
	}
}

func (stub *StreamStub) idleMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastAny := stub.lastActivityTime.Load().(time.Time)
			if stub.fastConnect {
				if time.Since(lastAny) > 30*time.Second {
					fmt.Println("Idle timeout, closing stream:", stub.myId)
					stub.doClose()
					return
				}
			}
		}
	}
}

func (stub *StreamStub) StartIdleMonitor() {
	go stub.idleMonitorLoop(stub.cancelCtx)
}

func (stub *StreamStub) StartSendStub() {
	go stub.sendDataLoop(stub.cancelCtx)
}

func (stub *StreamStub) StartRecvStub() {
	go stub.writeLoop(stub.cancelCtx)
}

func (stub *StreamStub) SetFastConnect(status bool) {
	stub.fastConnect = status
}

func (stub *StreamStub) SetDelayedResend(status bool) {
	stub.delayedResend = status
}

func (stub *StreamStub) SetDelayedResendRequest(status bool) {
	stub.delayedResendRequest = status
}

func (stub *StreamStub) StartSendLoop() {
	stub.startReadFromConn()
	go stub.sendDataLoop(stub.cancelCtx)
}

func (stub *StreamStub) StartRecvLoop() {
	go stub.writeLoop(stub.cancelCtx)
}

func (stub *StreamStub) lazyClose(timeout time.Duration) {
	stub.lazyCloseOnce.Do(func() {
		stub.DebugPrint("lazyClose", "Initiated")
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stub.cancelCtx.Done():
					return
				case <-ticker.C:
					lastAny := stub.lastActivityTime.Load().(time.Time)
					if time.Since(lastAny) > timeout {
						stub.DebugPrint("lazyClose", "Closing due to inactivity")
						stub.cancel()
						stub.conn.Close()
						stub.node.StubManager.DeleteStubById(stub.myId)
						return
					}
				}
			}
		}()
	})
}
