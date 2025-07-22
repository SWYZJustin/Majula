package core

import (
	"Majula/common"
	"fmt"
	"sync"
	"time"

	"github.com/xtaci/kcp-go"
	"golang.org/x/time/rate"
)

// KcpLink 表示一个基于KCP的连接，封装了会话、写队列和连接状态等信息。
type KcpLink struct {
	Sess                     *kcp.UDPSession
	StartTime                int64
	HasSendRegister          bool
	HasRecvRegister          bool
	Accepted                 bool
	ImportantWriteChan       chan []byte
	NormalWriteChan          chan []byte
	DropNormalPkgOnQueueFull bool
}

// Close 关闭当前KcpLink的KCP会话。
// 返回值：关闭成功返回true，否则返回false。
func (this *KcpLink) Close() bool {
	err := this.Sess.Close()
	if err != nil {
		return false
	}
	return true
}

// KCPChannelWorker 负责管理所有KCP连接，处理连接的注册、注销、消息收发、清理等。
type KCPChannelWorker struct {
	Name                   string
	Listener               *kcp.Listener
	LinksAddrFromPeer      map[string][]string
	KcpLinks               map[string]*KcpLink
	KcpLinkLastActiveTimes map[string]int64
	MutexForKcpLinks       sync.RWMutex
	MaxFrameSize           uint32
	RecvPackagesChan       chan IpPackage
	IsClosed               bool
	User                   ChannelUser
	messageCount           int64
	ipWhitelist            []string
	Token                  string
	MaxSendQueueSize       int
	MaxConnectionPerSecond int
	MaxInactiveTime        int64
	MaxRegistrationTime    int64
	Done                   chan bool
}

// getID 返回当前KCPChannelWorker的唯一标识。
func (CWorker *KCPChannelWorker) getID() string {
	return CWorker.Name
}

// NewKCPChannelWorker 创建一个新的KCPChannelWorker实例。
// 参数：
//
//	name - worker名称
//	isClient - 是否为客户端模式
//	localAddr - 本地监听地址（仅服务端）
//	remoteAddr - 远端地址（仅客户端）
//	maxFrameSize - 最大帧长度
//	maxInactiveDlt - 最大不活跃时间
//	maxSendQueueSize - 发送队列最大长度
//	maxConnectionPerSeconds - 每秒最大连接数
//
// 返回：*KCPChannelWorker 实例
func NewKCPChannelWorker(name string, isClient bool, localAddr string, remoteAddr string,
	ipWhitelist []string, maxFrameSize int, maxInactiveDlt int64, maxSendQueueSize int, maxConnectionPerSeconds int, token string) *KCPChannelWorker {

	var listener *kcp.Listener
	var err error
	if !isClient {
		listener, err = kcp.ListenWithOptions(localAddr, nil, 10, 3)
		if err != nil {
			return nil
		}
	}
	ret := &KCPChannelWorker{
		Name:                   name,
		MaxFrameSize:           uint32(maxFrameSize),
		LinksAddrFromPeer:      map[string][]string{},
		KcpLinks:               map[string]*KcpLink{},
		KcpLinkLastActiveTimes: map[string]int64{},
		ipWhitelist:            ipWhitelist,
		Token:                  token,
		MaxSendQueueSize:       maxSendQueueSize,
		MaxConnectionPerSecond: maxConnectionPerSeconds,
		MaxInactiveTime:        maxInactiveDlt,
		MaxRegistrationTime:    30, // default 30s
		Done:                   make(chan bool),
	}
	ret.RecvPackagesChan = make(chan IpPackage, 1024)
	if !isClient {
		ret.Listener = listener
		go ret.AcceptThread()
	} else {
		go ret.ConnectThread(remoteAddr)
	}

	go ret.StartCleanupThread()
	go ret.StartMessageProcessor()
	return ret
}

// AcceptThread 监听并接受新的KCP连接，超出速率限制的连接会被拒绝。
func (this *KCPChannelWorker) AcceptThread() {
	var acceptlimiter = rate.NewLimiter(rate.Limit(this.MaxConnectionPerSecond), this.MaxConnectionPerSecond*2)
	for !this.IsClosed {
		sess, err := this.Listener.AcceptKCP()
		if err != nil {
			break
		}
		//fmt.Printf("[KCP AcceptThread] New connection from %s\n", sess.RemoteAddr().String())
		// 白名单校验
		if len(this.ipWhitelist) > 0 {
			remoteAddr := sess.RemoteAddr().String()
			remoteIP := remoteAddr
			if idx := len(remoteAddr); idx > 0 {
				if colon := len(remoteAddr) - len(":0"); colon > 0 && remoteAddr[colon] == ':' {
					remoteIP = remoteAddr[:colon]
				} else if colon := len(remoteAddr) - len(":00"); colon > 0 && remoteAddr[colon] == ':' {
					remoteIP = remoteAddr[:colon]
				} else if colon := len(remoteAddr) - len(":0000"); colon > 0 && remoteAddr[colon] == ':' {
					remoteIP = remoteAddr[:colon]
				}
			}
			allowed := false
			for _, ip := range this.ipWhitelist {
				if remoteIP == ip {
					allowed = true
					break
				}
			}
			if !allowed {
				//fmt.Printf("[KCP AcceptThread] Connection from %s rejected by whitelist\n", remoteIP)
				sess.Close()
				continue
			}
		}
		if this.MaxConnectionPerSecond > 0 && !acceptlimiter.Allow() {
			//fmt.Printf("[KCP AcceptThread] Connection from %s rejected by rate limit\n", sess.RemoteAddr().String())
			sess.Close()
			continue
		}
		//fmt.Printf("[KCP AcceptThread] Connection from %s accepted\n", sess.RemoteAddr().String())
		//fmt.Printf("The channel user is nil? ")
		//fmt.Println(this.User == nil)
		if this.User != nil {
			this.User.onConnectChanged(this, true)
		}
		go this.ReadThread(sess, true)
	}
}

// wrapToDataFrame 将数据分帧封装，便于KCP传输。
// 参数：data - 原始数据
// 返回：分帧后的字节流
func (w *KCPChannelWorker) wrapToDataFrame(data []byte) []byte {
	maxDataSize := int(w.MaxFrameSize) - 4
	n := len(data)
	estimatedFrameCount := (n + maxDataSize - 1) / maxDataSize
	result := make([]byte, 0, n+estimatedFrameCount*4)
	for len(data) > 0 {
		chunkSize := len(data)
		if chunkSize > maxDataSize {
			chunkSize = maxDataSize
		}
		var frameType byte = 2
		if len(data) > maxDataSize {
			frameType = 4
		}
		result = append(result,
			frameType,
			byte(chunkSize>>16), byte(chunkSize>>8), byte(chunkSize),
		)
		result = append(result, data[:chunkSize]...)
		data = data[chunkSize:]
	}
	return result
}

// RegisterKcpLink 注册一个新的KCP连接，并启动写线程。
// 参数：sess - KCP会话，accepted - 是否为被动接受
// 返回：*KcpLink 新建的连接对象
func (this *KCPChannelWorker) RegisterKcpLink(sess *kcp.UDPSession, accepted bool) *KcpLink {
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	//fmt.Printf("[KCP RegisterKcpLink] Register %s, accepted=%v\n", sess.RemoteAddr().String(), accepted)
	oldSess, ok := this.KcpLinks[sess.RemoteAddr().String()]
	link := &KcpLink{
		StartTime:                time.Now().Unix(),
		Sess:                     sess,
		Accepted:                 accepted,
		ImportantWriteChan:       make(chan []byte, 128),
		NormalWriteChan:          make(chan []byte, 128),
		DropNormalPkgOnQueueFull: this.MaxSendQueueSize > 0,
	}
	this.KcpLinks[sess.RemoteAddr().String()] = link
	this.KcpLinkLastActiveTimes[sess.RemoteAddr().String()] = time.Now().Unix()
	go link.WriteThread()
	if ok {
		oldSess.Close()
	}
	return link
}

// UnregisterKcpLink 注销并关闭指定的KCP连接。
// 参数：sess - KCP会话
func (this *KCPChannelWorker) UnregisterKcpLink(sess *kcp.UDPSession) {
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	//fmt.Printf("[KCP UnregisterKcpLink] Unregister %s\n", sess.RemoteAddr().String())
	if oldSess, ok := this.KcpLinks[sess.RemoteAddr().String()]; ok {
		delete(this.KcpLinks, sess.RemoteAddr().String())
		oldSess.Close()
	}
}

// TouchKcpLink 更新指定KCP连接的活跃时间。
// 参数：sess - KCP会话
func (this *KCPChannelWorker) TouchKcpLink(sess *kcp.UDPSession) {
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	this.KcpLinkLastActiveTimes[sess.RemoteAddr().String()] = time.Now().Unix()
}

// ReadThread 负责从KCP连接读取数据包并分帧，推送到接收队列。
// 参数：sess - KCP会话，accepted - 是否为被动接受
func (this *KCPChannelWorker) ReadThread(sess *kcp.UDPSession, accepted bool) {
	//fmt.Printf("[KCP ReadThread] Start for %s, accepted=%v\n", sess.RemoteAddr().String(), accepted)
	link := this.RegisterKcpLink(sess, accepted)
	defer func() {
		this.UnregisterKcpLink(sess)
		if this.User != nil {
			this.User.onConnectChanged(this, false)
		}
		//fmt.Printf("[KCP ReadThread] Closed for %s\n", sess.RemoteAddr().String())
	}()
	head := make([]byte, 4)
	toBeContinueBuffer := []byte{}
	for !this.IsClosed {
		this.trySendRegisterMessage(link)
		i := 0
		for i < len(head) {
			n, err := sess.Read(head[i:])
			if err != nil {
				//fmt.Printf("[KCP ReadThread] Read head error: %v\n", err)
				return
			}
			i += n
			this.TouchKcpLink(sess)
		}
		length := (uint32(head[1]) << 16) | (uint32(head[2]) << 8) | uint32(head[3])
		ba := make([]byte, int(length))
		i = 0
		for i < len(ba) {
			n, err := sess.Read(ba[i:])
			if err != nil {
				//fmt.Printf("[KCP ReadThread] Read body error: %v\n", err)
				return
			}
			i += n
			this.TouchKcpLink(sess)
		}
		toBeContinueBuffer = append(toBeContinueBuffer, ba...)
		if head[0] == 2 {
			//fmt.Printf("[KCP ReadThread] Received data frame from %s, len=%d\n", sess.RemoteAddr().String(), len(toBeContinueBuffer))
			func() {
				defer func() { recover() }()
				this.RecvPackagesChan <- IpPackage{
					RemoteAddr: sess.RemoteAddr().String(),
					Data:       toBeContinueBuffer,
				}
			}()
			toBeContinueBuffer = []byte{}
		}
	}
}

// 尝试发送注册消息。
// 参数：link - KCP连接。
func (this *KCPChannelWorker) trySendRegisterMessage(link *KcpLink) {
	if this.User == nil || link.HasSendRegister {
		return
	}
	link.HasSendRegister = true
	RegisterMsg := Message{
		MessageData: MessageData{
			Type: TcpRegister, // 沿用TcpRegister类型
			Data: HashIDWithToken(this.User.getID(), this.Token),
		},
		From: this.User.getID(),
		To:   "",
	}
	sentItem, err := common.MarshalAny(RegisterMsg)
	if err != nil {
		fmt.Printf("[KCP trySendRegisterMessage] Marshal error: %v\n", err)
		return
	}
	//fmt.Printf("[KCP trySendRegisterMessage] Send register from %s\n", this.User.getID())
	link.WriteTo(this.wrapToDataFrame(sentItem), true)
}

// WriteTo 将数据写入KcpLink的写队列。
// 参数：ba - 数据，important - 是否为重要包
// 返回值：写入成功返回true
func (link *KcpLink) WriteTo(ba []byte, important bool) (writeOk bool) {
	writeOk = false
	defer func() { recover() }()
	if important {
		link.ImportantWriteChan <- ba
	} else if !link.DropNormalPkgOnQueueFull || len(link.NormalWriteChan) < cap(link.NormalWriteChan) {
		link.NormalWriteChan <- ba
	}
	writeOk = true
	return writeOk
}

// WriteThread 负责从写队列中取出数据并写入KCP连接。
func (link *KcpLink) WriteThread() {
	const importantQuota = 5
	importantCount := 0
	for {
		if importantCount < importantQuota {
			select {
			case ba, ok := <-link.ImportantWriteChan:
				if !ok {
					return
				}
				writeAllKCP(link.Sess, ba)
				importantCount++
			case ba, ok := <-link.NormalWriteChan:
				if !ok {
					return
				}
				writeAllKCP(link.Sess, ba)
				importantCount = 0
			}
		} else {
			select {
			case ba, ok := <-link.NormalWriteChan:
				if !ok {
					return
				}
				writeAllKCP(link.Sess, ba)
			case ba, ok := <-link.ImportantWriteChan:
				if !ok {
					return
				}
				writeAllKCP(link.Sess, ba)
			}
			importantCount = 0
		}
	}
}

// writeAllKCP 将所有数据写入KCP会话，直到写完或出错。
// 参数：sess - KCP会话，ba - 数据
func writeAllKCP(sess *kcp.UDPSession, ba []byte) {
	total := 0
	for total < len(ba) {
		n, err := sess.Write(ba[total:])
		if err != nil {
			return
		}
		total += n
	}
}

// DeleteInactiveKcpLink 清理不活跃或未注册的KCP连接。
// 返回值：被清理的KCP会话列表
func (this *KCPChannelWorker) DeleteInactiveKcpLink() []*kcp.UDPSession {
	ret := []*kcp.UDPSession{}
	now := time.Now().Unix()
	inactiveKeys := []string{}
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	for k, v := range this.KcpLinks {
		if lastActiveTime, ok := this.KcpLinkLastActiveTimes[k]; ok {
			if lastActiveTime+this.MaxInactiveTime < now {
				inactiveKeys = append(inactiveKeys, k)
				continue
			}
		}
		if !v.HasRecvRegister && v.StartTime+this.MaxRegistrationTime < now {
			inactiveKeys = append(inactiveKeys, k)
		}
	}
	for _, k := range inactiveKeys {
		if link, exists := this.KcpLinks[k]; exists {
			ret = append(ret, link.Sess)
			delete(this.KcpLinks, k)
			delete(this.KcpLinkLastActiveTimes, k)
		}
	}
	return ret
}

// ConnectThread 客户端模式下，负责主动连接远端KCP服务端，并自动重连。
// 参数：remoteAddr - 远端地址
func (this *KCPChannelWorker) ConnectThread(remoteAddr string) {
	connectFailureCount := 0
	for !this.IsClosed {
		this.MutexForKcpLinks.RLock()
		link, ok := this.KcpLinks[remoteAddr]
		this.MutexForKcpLinks.RUnlock()
		var sess *kcp.UDPSession
		if ok {
			sess = link.Sess
		} else {
			if this.IsClosed {
				return
			}
			sess, _ = kcp.DialWithOptions(remoteAddr, nil, 10, 3)
			if sess != nil {
				if this.User != nil {
					this.User.onConnectChanged(this, true)
				}
				go this.ReadThread(sess, false)
			}
		}
		if sess != nil {
			connectFailureCount = 0
		} else {
			if connectFailureCount < 50 {
				connectFailureCount++
			}
		}
		var wait time.Duration
		if connectFailureCount > 0 {
			backoff := time.Duration(1<<uint(connectFailureCount)) * 100 * time.Millisecond
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
			wait = backoff
		} else {
			wait = 100 * time.Millisecond
		}
		time.Sleep(wait)
	}
}

// StartCleanupThread 定期清理不活跃的KCP连接。
func (this *KCPChannelWorker) StartCleanupThread() {
	for {
		select {
		case <-this.Done:
			return
		case <-time.After(time.Second):
			for _, sess := range this.DeleteInactiveKcpLink() {
				sess.Close()
			}
		}
	}
}

// StartMessageProcessor 启动消息处理主循环，将接收到的包分发给业务层。
func (this *KCPChannelWorker) StartMessageProcessor() {
	for {
		select {
		case <-this.Done:
			return
		case pkg := <-this.RecvPackagesChan:
			this.processPackage(pkg)
		}
	}
}

// processPackage 处理收到的IpPackage，反序列化为Message并分发。
// 参数：pkg - 接收到的包
func (this *KCPChannelWorker) processPackage(pkg IpPackage) {
	pra := pkg.RemoteAddr
	if len(pkg.Data) > 0 {
		var msg Message
		err := common.UnmarshalAny(pkg.Data, &msg)
		//fmt.Println("I received a message " + msg.Print())
		if err != nil {
			return
		}
		if msg.Type == TcpRegister {
			this.handleRegisterMessage(pra, &msg)
		} else {
			this.handleNormalMessage(pra, &msg)
		}
	} else {
		return
	}
}

// checkHash 校验消息哈希。
// 参数：msg - 消息
// 返回值：校验结果
func (this *KCPChannelWorker) checkHash(msg *Message) bool {
	HashValue := msg.Data
	selfCalculatedHash := HashIDWithToken(msg.From, this.Token)
	result := selfCalculatedHash == HashValue
	//fmt.Printf("[KCP checkHash] From: %s, Expected: %s, Actual: %s, Result: %v\n", msg.From, selfCalculatedHash, HashValue, result)
	return result
}

// handleRegisterMessage 处理注册消息，标记连接已注册。
// 参数：pra - 远端地址，msg - 消息
func (this *KCPChannelWorker) handleRegisterMessage(pra string, msg *Message) {
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	//fmt.Printf("[KCP handleRegisterMessage] Received register from %s, hash ok: %v\n", msg.From, this.checkHash(msg))
	if this.checkHash(msg) != true || msg.From == this.User.getID() {
		if link, ok := this.KcpLinks[pra]; ok && link != nil {
			//fmt.Printf("[KCP handleRegisterMessage] Close link %s due to hash or self\n", pra)
			link.Close()
		}
		return
	}
	if link, ok := this.KcpLinks[pra]; ok && link != nil {
		link.HasRecvRegister = true
		//fmt.Printf("[KCP handleRegisterMessage] Register success for %s\n", pra)
	}
}

// handleNormalMessage 处理普通消息，更新路由表并分发给业务层。
// 参数：pra - 远端地址，msg - 消息
func (this *KCPChannelWorker) handleNormalMessage(pra string, msg *Message) {
	peerId := msg.LastSender
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	if _, ok := this.LinksAddrFromPeer[peerId]; !ok {
		this.LinksAddrFromPeer[peerId] = []string{}
	}
	match := false
	for i, ra := range this.LinksAddrFromPeer[peerId] {
		if ra == pra {
			match = true
			if i != 0 {
				this.LinksAddrFromPeer[peerId][i] = this.LinksAddrFromPeer[peerId][0]
				this.LinksAddrFromPeer[peerId][0] = pra
			}
			break
		}
	}
	if !match {
		this.LinksAddrFromPeer[peerId] = append([]string{pra}, this.LinksAddrFromPeer[peerId]...)
	}
	if user := this.User; user != nil {
		go user.onRecv(peerId, msg)
	}
}

// Close 关闭KCPChannelWorker，释放所有资源。
// 返回值：错误信息（如有）
func (this *KCPChannelWorker) Close() error {
	this.IsClosed = true
	close(this.Done)
	if this.Listener != nil {
		this.Listener.Close()
	}
	for _, sess := range this.DeleteInactiveKcpLinkForce() {
		sess.Close()
	}
	close(this.RecvPackagesChan)
	return nil
}

// DeleteInactiveKcpLinkForce 强制清理所有KCP连接。
// 返回值：被清理的KCP会话列表
func (this *KCPChannelWorker) DeleteInactiveKcpLinkForce() []*kcp.UDPSession {
	this.MutexForKcpLinks.Lock()
	defer this.MutexForKcpLinks.Unlock()
	ret := []*kcp.UDPSession{}
	for k, link := range this.KcpLinks {
		ret = append(ret, link.Sess)
		delete(this.KcpLinks, k)
		delete(this.KcpLinkLastActiveTimes, k)
	}
	return ret
}

// sendTo 向指定节点发送消息。
// 参数：nextHopNodeId - 目标节点ID，msg - 消息
func (this *KCPChannelWorker) sendTo(nextHopNodeId string, msg *Message) {
	if msg == nil {
		return
	}
	this.MutexForKcpLinks.RLock()
	if ras, ok := this.LinksAddrFromPeer[nextHopNodeId]; ok {
		sentItem, _ := common.MarshalAny(msg)
		ba := this.wrapToDataFrame(sentItem)
		for _, ra := range ras {
			if link, ok := this.KcpLinks[ra]; ok {
				if !link.HasRecvRegister {
					//fmt.Println("KCP has not receive the register!")
					continue
				}
				go func(ba []byte, link *KcpLink, important bool) {
					if len(ba) > 0 {
						link.WriteTo(ba, important)
					}
				}(ba, link, msg.isImportant())
			}
		}
	}
	this.MutexForKcpLinks.RUnlock()
}

// broadCast 向所有已注册的KCP连接广播消息。
// 参数：msg - 消息
func (this *KCPChannelWorker) broadCast(msg *Message) {
	if msg == nil {
		return
	}
	sentItem, _ := common.MarshalAny(msg)
	go func(ba []byte, important bool) {
		if len(ba) > 0 {
			links := this.GetAllConns()
			for _, link := range links {
				if !link.HasRecvRegister && msg.Type != TcpRegister {
					//fmt.Println("KCP has not receive the register for broadcast!")
					continue
				}
				go func(link *KcpLink) {
					link.WriteTo(ba, important)
				}(link)
			}
		}
	}(this.wrapToDataFrame(sentItem), msg.isImportant())
}

// GetAllConns 获取所有当前活跃的KcpLink。
// 返回值：KcpLink列表
func (this *KCPChannelWorker) GetAllConns() []*KcpLink {
	ret := []*KcpLink{}
	this.MutexForKcpLinks.RLock()
	defer this.MutexForKcpLinks.RUnlock()
	for _, v := range this.KcpLinks {
		ret = append(ret, v)
	}
	return ret
}
