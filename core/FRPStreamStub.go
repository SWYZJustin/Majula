package core

import (
	"Majula/common"
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DebugPrint StreamStub的调试打印函数。
// 参数：name - 调用者名称，message - 要输出的信息。
func (s *StreamStub) DebugPrint(name string, message string) {
	return
	fmt.Printf("[%s: %s] %s\n", s.myId, name, message)
}

// WindowBuffer WindowBuffer结构体，滑动窗口缓冲区，用于流式数据的有序缓存和重传。
type WindowBuffer struct {
	mu         sync.Mutex
	startSeq   int64
	size       int
	data       [][]byte
	filled     []bool
	retryCount []int
	sendTime   []time.Time
}

// NewWindowBuffer 创建一个新的WindowBuffer实例。
// 参数：startSeq - 起始序号，size - 窗口大小。
// 返回：*WindowBuffer 新建的窗口缓冲区。
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

// Put 向窗口缓冲区中放入数据。
// 参数：seq - 数据序号，value - 数据内容。
// 返回：是否成功放入。
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

// Get 从窗口缓冲区获取指定序号的数据。
// 参数：seq - 数据序号。
// 返回：数据内容和是否存在。
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

// Advance 推进窗口起始序号，移除最前的数据。
// 返回：是否成功推进。
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

// BundleAdvanceUpTo 批量推进窗口到指定最大序号，移除已填充的数据。
// 参数：maxSeq - 最大推进到的序号。
// 返回：实际推进的数量。
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

// IsFilled 判断指定序号的数据是否已填充。
// 参数：seq - 数据序号。
// 返回：是否已填充。
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

// GetWithMeta 获取指定序号的数据及其元信息。
// 参数：seq - 数据序号。
// 返回：数据内容、重试次数、发送时间、是否存在。
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

// IncrementRetry 增加指定序号的数据重试次数并更新时间。
// 参数：seq - 数据序号。
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

// RemoveUpTo 移除窗口中直到指定序号（含）之前的所有数据。
// 参数：seq - 序号。
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

// IsFull 判断窗口是否已满。
// 返回：是否已满。
func (wb *WindowBuffer) IsFull() bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return wb.Count() >= wb.size
}

// Count 统计窗口中已填充的数据数量。
// 返回：已填充数量。
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

// CanPut 判断指定序号是否可以放入窗口。
// 参数：seq - 数据序号。
// 返回：是否可以放入。
func (wb *WindowBuffer) CanPut(seq int64) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return seq >= wb.startSeq && seq < wb.startSeq+int64(wb.size)
}

// FRPDataPayload FRP数据包结构体。
type FRPDataPayload struct {
	TargetStubID string `json:"stub_id"`
	Seq          int64  `json:"seq"`
	Data         []byte `json:"data"`
}

// FRP数据包转字符串。
func (p FRPDataPayload) String() string {
	return fmt.Sprintf("FRPDataPayload{TargetStubID: %s, Seq: %d, Data: %q}", p.TargetStubID, p.Seq, p.Data)
}

// FRPAckPayload FRP确认包结构体。
type FRPAckPayload struct {
	TargetStubID string `json:"stub_id"`
	Ack          int64  `json:"ack"`
}

// FRPResendRequestPayload FRP重传请求包结构体。
type FRPResendRequestPayload struct {
	TargetStubID string `json:"stub_id"`
	Seq          int64  `json:"seq"`
}

// FRPCloseAckPayload FRP关闭确认包结构体。
type FRPCloseAckPayload struct {
	TargetStubID string `json:"stub_id"`
}

// FRP确认包转字符串。
func (p FRPAckPayload) String() string {
	return fmt.Sprintf("FRPAckPayload{TargetStubID: %s, Ack: %d}", p.TargetStubID, p.Ack)
}

// FRP重传请求包转字符串。
func (p FRPResendRequestPayload) String() string {
	return fmt.Sprintf("FRPResendRequestPayload{TargetStubID: %s, Seq: %d}", p.TargetStubID, p.Seq)
}

// FRP关闭确认包转字符串。
func (p FRPCloseAckPayload) String() string {
	return fmt.Sprintf("FRPCloseAckPayload{TargetStubID: %s}", p.TargetStubID)
}

// frpMessage结构体，表示内部消息。
type frpMessage struct {
	msgType MessageType
	payload []byte
}

// StreamStub StreamStub结构体，表示一个FRP流的端点，负责数据收发、重传、窗口管理等。
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

// 发送数据主循环，从readChan读取数据并发送，管理发送窗口。
// 参数：ctx - 上下文，用于取消。
func (stub *StreamStub) sendDataLoop(ctx context.Context) {
	stub.DebugPrint("startSendDataLoop", stub.myId)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("sendDataLoop cancelled")
			stub.doClose()
			return
		case data := <-stub.readChan:
			//fmt.Printf("[FRP sendDataLoop] readChan got data, len=%d\n", len(data))
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

// 实际发送一条数据到对端。
// 参数：seq - 序号，data - 数据内容。
func (stub *StreamStub) sendData(seq int64, data []byte) {
	//fmt.Printf("[FRP sendData] seq=%d, len=%d\n", seq, len(data))
	stub.lastActivityTime.Store(time.Now())
	payload := FRPDataPayload{
		TargetStubID: stub.peerStubId,
		Seq:          seq,
		Data:         data,
	}
	payloadJSON, _ := common.MarshalAny(payload)

	msg := &Message{
		MessageData: MessageData{
			Type: FRPData,
			Data: string(payloadJSON),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        common.DefaultMessageTTL,
		LastSender: stub.myNodeId,
	}

	stub.DebugPrint("stub "+stub.myId+"sendData", msg.Print())

	go stub.node.sendTo(stub.peerNodeId, msg)
}

// 处理接收到的数据包，写入接收窗口并尝试顺序写出。
// 参数：content - 数据内容。
func (stub *StreamStub) onData(content []byte) {
	stub.lastActivityTime.Store(time.Now())
	var payload FRPDataPayload
	if err := common.UnmarshalAny(content, &payload); err != nil || payload.TargetStubID != stub.myId {
		stub.DebugPrint("stub "+stub.myId+"onData", "data error")
		return
	}
	//fmt.Printf("[FRP onData] payload.Seq=%d, len=%d\n", payload.Seq, len(payload.Data))
	stub.DebugPrint("stub "+stub.myId+"onData", payload.String())

	stub.recvWindowLock.Lock()
	defer stub.recvWindowLock.Unlock()

	if !stub.recvWindow.Put(payload.Seq, payload.Data) {
		return
	}

	progress := false
	startSeq := stub.recvSeq + 1
	for seq := startSeq; seq < startSeq+int64(common.MaxSendWindowSize); seq++ {
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
		for seq := stub.recvSeq + 1; seq < payload.Seq && sent < common.MaxResendPerCall; seq++ {
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
		if stub.recvSinceLastAck >= int64(common.RecvAckThreshold) || now.Sub(stub.lastAckTime) >= common.RecvAckTimeout {
			stub.sendAck()
			stub.recvSinceLastAck = 0
			stub.lastAckTime = now
		}
	default:
		stub.sendAck()
	}
}

// 发送确认包（Ack）给对端。
func (stub *StreamStub) sendAck() {
	stub.lastActivityTime.Store(time.Now())
	ackPayload := FRPAckPayload{
		TargetStubID: stub.peerStubId,
		Ack:          stub.recvSeq,
	}
	ackData, _ := common.MarshalAny(ackPayload)

	ackMsg := &Message{
		MessageData: MessageData{
			Type: FRPAck,
			Data: string(ackData),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        common.DefaultMessageTTL,
		LastSender: stub.myNodeId,
	}
	stub.DebugPrint("stub "+stub.myId+"sendAck", ackMsg.Print())
	stub.node.sendTo(stub.peerNodeId, ackMsg)
}

// 处理收到的Ack包，推进发送窗口。
// 参数：content - Ack包内容。
func (stub *StreamStub) onAck(content []byte) {
	stub.lastActivityTime.Store(time.Now())
	var payload FRPAckPayload
	if err := common.UnmarshalAny(content, &payload); err != nil || payload.TargetStubID != stub.myId {
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

// 处理收到的关闭请求，执行关闭操作。
func (stub *StreamStub) onClose() {
	stub.DebugPrint("stub "+stub.myId+"close", "")
	stub.lazyClose(common.AckTimeout)
}

// 执行流的关闭操作，释放资源。
func (stub *StreamStub) doClose() {
	stub.lastActivityTime.Store(time.Now())
	msg := &Message{
		MessageData: MessageData{
			Type: FRPClose,
			Data: stub.myId,
		},
		From: stub.myNodeId,
		To:   stub.peerNodeId,
		TTL:  common.DefaultMessageTTL,
	}
	stub.DebugPrint("stub "+stub.myId+"sendClose", msg.Print())
	stub.node.sendTo(stub.peerNodeId, msg)
	stub.lazyClose(common.AckTimeout)
}

// 重传检测主循环，定时检查未确认的数据并重发。
// 参数：ctx - 上下文，用于取消。
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

// 启动重传检测协程。
func (stub *StreamStub) startRetryLoop() {
	go stub.retryLoop(stub.cancelCtx)
}

// 重新发送所有未被确认的数据包。
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
			if retries >= common.MaxRetryCount {
				fmt.Println("Too many retries, closing stream:", stub.myId)
				fmt.Println("conn close due to too many retries")
				stub.doClose()
				return
			}
		}

		if stub.delayedResend {
			if time.Since(sentAt) < common.AckTimeout {
				continue
			}
		}
		stub.sendWindow.IncrementRetry(seq)
		stub.sendData(seq, data)
	}

}

// NewStreamStub 创建一个新的StreamStub实例。
// 参数：node - 所属节点，conn - 连接，myId/peerNodeId/peerStubId/myNodeId - 标识信息。
// 返回：*StreamStub 新建的流对象。
func NewStreamStub(node *Node, conn net.Conn, myId, peerNodeId, peerStubId, myNodeId string) *StreamStub {
	ctx, cancel := context.WithCancel(context.Background())

	stub := &StreamStub{
		conn:       conn,
		node:       node,
		myId:       myId,
		peerNodeId: peerNodeId,
		peerStubId: peerStubId,
		myNodeId:   myNodeId,
		recvWindow: NewWindowBuffer(1, common.MaxSendWindowSize),
		sendWindow: NewWindowBuffer(1, common.MaxSendWindowSize),

		cancel:           cancel,
		recvSinceLastAck: 0,
		lastAckTime:      time.Now(),
		cancelCtx:        ctx,
		sendWindowLock:   sync.Mutex{},

		resendRequestedAt: make(map[int64]time.Time),
		writeChan:         make(chan []byte, common.ChannelQueueSizeLarge),

		fastConnect:          false,
		delayedResend:        false,
		delayedResendRequest: false,

		ackMode: "immediate",

		inChan:        make(chan frpMessage, common.ChannelQueueSizeLarge),
		lazyCloseOnce: sync.Once{},
		readChan:      make(chan []byte, common.ChannelQueueSizeLarge),
	}
	stub.lastActivityTime.Store(time.Now())
	stub.windowCond = sync.NewCond(&stub.sendWindowLock)

	go stub.frpMessageInLoop()

	return stub
}

// 启动从底层连接读取数据的协程。
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
				//fmt.Printf("[FRP startReadFromConn] read data, len=%d\n", n)
				data := make([]byte, n)
				copy(data, buf[:n])

				select {
				case stub.readChan <- data:
					//fmt.Printf("[FRP startReadFromConn] data pushed to readChan\n")
				case <-stub.cancelCtx.Done():
					return
				}
			}
		}
	}()
}

// 处理frp消息输入主循环。
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

// 发送重传请求给对端。
// 参数：seq - 需要重传的数据序号。
func (stub *StreamStub) sendResendRequest(seq int64) {
	stub.lastActivityTime.Store(time.Now())
	stub.sendWindowLock.Lock()
	lastTime, requested := stub.resendRequestedAt[seq]
	if stub.delayedResendRequest {
		if requested && time.Since(lastTime) < common.SendResendTimeout {
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
	data, _ := common.MarshalAny(req)

	msg := &Message{
		MessageData: MessageData{
			Type: FRPResendRequest,
			Data: string(data),
		},
		From:       stub.myNodeId,
		To:         stub.peerNodeId,
		TTL:        common.DefaultMessageTTL,
		LastSender: stub.myNodeId,
	}
	stub.DebugPrint("stub "+stub.myId+"sendResendRequest", msg.Print())
	stub.node.sendTo(stub.peerNodeId, msg)
}

// 处理收到的重传请求，尝试重发指定序号的数据。
// 参数：content - 重传请求内容。
func (stub *StreamStub) onResendRequest(content []byte) {
	stub.lastActivityTime.Store(time.Now())

	var payload FRPResendRequestPayload
	if err := common.UnmarshalAny(content, &payload); err != nil || payload.TargetStubID != stub.myId {
		stub.DebugPrint("stub "+stub.myId+"onResendRequest", "data error")
		return
	}
	stub.DebugPrint("stub "+stub.myId+"sendResendRequest", payload.String())
	stub.sendWindowLock.Lock()
	data, _, sentAt, ok := stub.sendWindow.GetWithMeta(payload.Seq)
	stub.sendWindowLock.Unlock()
	if stub.delayedResend {
		if stub.delayedResend {
			if time.Since(sentAt) < common.AckTimeout {
				return
			}
		}
	}
	if ok {
		stub.sendData(payload.Seq, data)
	}
}

// 将数据加入写入队列。
// 参数：data - 要写入的数据。
func (stub *StreamStub) enqueueWrite(data []byte) {
	//fmt.Printf("[FRP enqueueWrite] data len=%d\n", len(data))
	select {
	case stub.writeChan <- data:
		//fmt.Printf("[FRP enqueueWrite] data enqueued\n")
	default:
		fmt.Printf("[FRP enqueueWrite] writeChan full, dropping data\n")
	}
}

// 写入主循环，将数据写入底层连接。
// 参数：ctx - 上下文，用于取消。
func (stub *StreamStub) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-stub.writeChan:
			//fmt.Printf("[FRP writeLoop] writing data, len=%d\n", len(data))
			_, err := stub.conn.Write(data)
			if err != nil {
				//fmt.Printf("[FRP writeLoop] Write error: %v\n", err)
				stub.doClose()
				return
			} else {
				stub.lastActivityTime.Store(time.Now())
			}
		}
	}
}

// 空闲监控主循环，检测连接是否长时间无活动。
// 参数：ctx - 上下文，用于取消。
func (stub *StreamStub) idleMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(common.AckTimeout)
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

// StartIdleMonitor 启动空闲监控。
func (stub *StreamStub) StartIdleMonitor() {
	go stub.idleMonitorLoop(stub.cancelCtx)
}

// StartSendStub 启动发送端Stub。
func (stub *StreamStub) StartSendStub() {
	go stub.sendDataLoop(stub.cancelCtx)
}

// StartRecvStub 启动接收端Stub。
func (stub *StreamStub) StartRecvStub() {
	go stub.writeLoop(stub.cancelCtx)
}

// SetFastConnect 设置快速连接模式。
// 参数：status - 是否启用。
func (stub *StreamStub) SetFastConnect(status bool) {
	stub.fastConnect = status
}

// SetDelayedResend 设置延迟重传模式。
// 参数：status - 是否启用。
func (stub *StreamStub) SetDelayedResend(status bool) {
	stub.delayedResend = status
}

// SetDelayedResendRequest 设置延迟重传请求模式。
// 参数：status - 是否启用。
func (stub *StreamStub) SetDelayedResendRequest(status bool) {
	stub.delayedResendRequest = status
}

// StartSendLoop 启动发送主循环。
func (stub *StreamStub) StartSendLoop() {
	stub.startReadFromConn()
	go stub.sendDataLoop(stub.cancelCtx)
}

// StartRecvLoop 启动接收主循环。
func (stub *StreamStub) StartRecvLoop() {
	go stub.writeLoop(stub.cancelCtx)
}

// 延迟关闭流，等待超时后关闭。
// 参数：timeout - 延迟关闭时间。
func (stub *StreamStub) lazyClose(timeout time.Duration) {
	stub.lazyCloseOnce.Do(func() {
		stub.DebugPrint("lazyClose", "Initiated")
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
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

// Close 关闭流，释放所有资源。
func (stub *StreamStub) Close() {
	stub.lazyClose(common.AckTimeout)
}

// 启动重传主循环。
func (stub *StreamStub) startResendLoop() {
	ticker := time.NewTicker(common.AckTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-stub.cancelCtx.Done():
			return
		case <-ticker.C:
			stub.resendUnackedData()
		}
	}
}
