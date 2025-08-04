package core

import (
	"Majula/common"
	"bufio"
	"crypto/tls"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type TcpLink struct {
	LinkConn                 net.Conn
	StartTime                int64
	HasSendRegister          bool
	HasRecvRegister          bool
	Accepted                 bool
	ImportantWriteChan       chan []byte
	NormalWriteChan          chan []byte
	DropNormalPkgOnQueueFull bool
	TlsServerName            map[string]interface{}
}

// Close 关闭TcpLink连接。
// 返回：是否关闭成功。
func (this *TcpLink) Close() bool {
	err := this.LinkConn.Close()
	if err != nil {
		return false
	}
	return true
}

type IpPackage struct {
	Data       []byte
	RemoteAddr string
}

type TcpChannelWorker struct {
	Name                   string
	LocalAddr              net.Addr
	LocalClientAddr        net.Addr
	Listener               net.Listener
	LinksAddrFromPeer      map[string][]string
	TcpLinks               map[string]*TcpLink
	TcpLinkLastActiveTimes map[string]int64
	MutexForTcpLinks       sync.RWMutex
	MaxFrameSize           uint32
	RecvPackagesChan       chan IpPackage
	IsClosed               bool
	User                   ChannelUser
	messageCount           int64
	ipWhitelist            []net.IP
	TlsConfig              *tls.Config
	Token                  string
	MaxSendQueueSize       int
	MaxConnectionPerSecond int
	MaxInactiveTime        int64
	MaxRegistrationTime    int64
	Done                   chan bool
}

// 获取TcpChannelWorker的唯一ID。
// 返回：名称字符串。
func (CWorker *TcpChannelWorker) getID() string {
	return CWorker.Name
}

// NewTcpConnection 创建新的TCP通道工作者
// 参数：name - 通道名称，isClient - 是否为客户端，localAddr - 本地地址，remoteAddr - 远程地址
// ipWhitelist - IP白名单，maxFrameSize - 最大帧大小，maxInactiveDlt - 最大非活跃时间
// maxSendQueueSize - 最大发送队列大小，maxConnectionPerSeconds - 每秒最大连接数
// tlsConfig - TLS配置，pToken - 令牌
// 返回：*TcpChannelWorker
func NewTcpConnection(name string, isClient bool, localAddr string, remoteAddr string,
	ipWhitelist []string, maxFrameSize int,
	maxInactiveDlt int64, maxSendQueueSize int, maxConnectionPerSeconds int, tlsConfig *tls.Config, pToken string) *TcpChannelWorker {

	localTcpaddr, err := net.ResolveTCPAddr("tcp", localAddr)
	if err != nil {
		if !isClient {
			Error("解析本地地址失败", "localAddr=", localAddr, "error=", err)
			return nil
		}
	}
	ss := strings.Split(localAddr, ":")
	var localClientTcpAddr *net.TCPAddr
	if len(ss) == 2 {
		localClientTcpAddr, err = net.ResolveTCPAddr("tcp", ss[0]+":0")
	}

	var listener net.Listener
	if !isClient {
		if tlsConfig == nil {
			listener, err = net.ListenTCP("tcp", localTcpaddr)
			if err != nil {
				Error("TCP监听失败", "localAddr=", localTcpaddr, "error=", err)
				return nil
			}
		} else {
			listener, err = tls.Listen("tcp", localTcpaddr.String(), tlsConfig)
			if err != nil {
				Error("TLS监听失败", "localAddr=", localTcpaddr, "error=", err)
				return nil
			}
		}
	}

	ipWhiteList := []net.IP{}
	for _, ip := range ipWhitelist {
		if nip, err := net.ResolveIPAddr("ip", ip); err == nil {
			ipWhiteList = append(ipWhiteList, nip.IP)
		}
	}
	ret := &TcpChannelWorker{
		Name:                   name,
		MaxFrameSize:           uint32(maxFrameSize),
		LinksAddrFromPeer:      map[string][]string{},
		TcpLinks:               map[string]*TcpLink{},
		TcpLinkLastActiveTimes: map[string]int64{},
		LocalAddr:              localTcpaddr,
		LocalClientAddr:        localClientTcpAddr,
		ipWhitelist:            ipWhiteList,
		TlsConfig:              tlsConfig,
		MaxSendQueueSize:       maxSendQueueSize,
		MaxConnectionPerSecond: maxConnectionPerSeconds,
		MaxInactiveTime:        maxInactiveDlt,
		MaxRegistrationTime:    int64(common.MaxRegistrationTime),
		Token:                  pToken,
		Done:                   make(chan bool),
	}
	ret.RecvPackagesChan = make(chan IpPackage, common.ChannelQueueSizeSmall)
	// 如果是server，就开始监听
	if !isClient {
		ret.Listener = listener
		go ret.AcceptThread()

		// 如果是client，就尝试连接
	} else {
		remoteTcpaddr, err := net.ResolveTCPAddr("tcp", remoteAddr)
		if err != nil {
			Error("解析远程地址失败", "remoteAddr=", remoteAddr, "error=", err)
			return nil
		}
		go ret.ConnectThread(localTcpaddr, remoteTcpaddr)
	}

	// 开启控制线程
	go ret.StartCleanupThread()
	go ret.StartMessageProcessor()
	return ret

}

// AcceptThread 监听并接受TCP连接的主线程
func (this *TcpChannelWorker) AcceptThread() {
	var acceptlimiter = rate.NewLimiter(rate.Limit(this.MaxConnectionPerSecond), this.MaxConnectionPerSecond*2) // 每秒连接控制

	for !this.IsClosed {
		conn, err := this.Listener.Accept()
		if err != nil {
			Error("接受连接失败", "error=", err)
			break
		}

		// 检查白名单
		if len(this.ipWhitelist) > 0 {
			matchFlag := false
			for _, ip := range this.ipWhitelist {
				if strings.Index(conn.RemoteAddr().String(), ip.String()) == 0 {
					matchFlag = true
					break
				}
			}
			if !matchFlag {
				conn.Close()
				continue
			}
		}

		if this.MaxConnectionPerSecond > 0 && !acceptlimiter.Allow() {
			conn.Close()
			continue
		}

		// 如果tsl连接成功，则在3秒内完成握手
		if tlsConn, ok := conn.(*tls.Conn); ok {
			err := tlsConn.SetDeadline(time.Now().Add(3 * time.Second))
			if err != nil {
				conn.Close()
				continue
			}

			err = tlsConn.Handshake()
			if err != nil {
				conn.Close()
				continue
			}

			// 清除超时时间
			_ = tlsConn.SetDeadline(time.Time{})
		}

		if this.User != nil {
			this.User.onConnectChanged(this, true)
		}
		go this.ReadThread(conn, false)
	}
}

// wrapToDataFrame 将数据分片打包为数据帧
// 参数：data - 原始数据
// 返回：分帧后的字节切片
func (w *TcpChannelWorker) wrapToDataFrame(data []byte) []byte {
	maxDataSize := int(w.MaxFrameSize) - 4
	n := len(data)

	// 预分配内存，估算最大可能长度
	estimatedFrameCount := (n + maxDataSize - 1) / maxDataSize
	result := make([]byte, 0, n+estimatedFrameCount*4)

	for len(data) > 0 {
		chunkSize := len(data)
		if chunkSize > maxDataSize {
			chunkSize = maxDataSize
		}

		// 是否是最后一帧
		var frameType byte = 2
		if len(data) > maxDataSize {
			frameType = 4
		}

		// 构建 4 字节帧头
		result = append(result,
			frameType,
			byte(chunkSize>>16), byte(chunkSize>>8), byte(chunkSize),
		)

		// 添加数据
		result = append(result, data[:chunkSize]...)
		data = data[chunkSize:]
	}

	return result
}

// RegisterTcpLink 注册一个TCP连接
// 参数：conn - 连接，accepted - 是否为被动接收
// 返回：*TcpLink
func (this *TcpChannelWorker) RegisterTcpLink(conn net.Conn, accepted bool) *TcpLink {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()
	oldConn, ok := this.TcpLinks[conn.RemoteAddr().String()]
	link := &TcpLink{
		StartTime:          time.Now().Unix(),
		LinkConn:           conn,
		Accepted:           accepted,
		ImportantWriteChan: make(chan []byte, common.ChannelQueueSizeSmall),
		NormalWriteChan: make(chan []byte, func() int {
			if this.MaxSendQueueSize <= 0 {
				return common.ChannelQueueSizeSmall
			}
			return this.MaxSendQueueSize
		}()),
		DropNormalPkgOnQueueFull: this.MaxSendQueueSize > 0,
		TlsServerName: func() map[string]interface{} {
			ret := make(map[string]interface{})
			if tlsConn, ok := conn.(*tls.Conn); ok {
				if tlsConn.ConnectionState().PeerCertificates != nil {
					for _, cert := range tlsConn.ConnectionState().PeerCertificates {
						ret[cert.Subject.CommonName] = nil
					}
				}
			}
			return ret
		}(),
	}
	this.TcpLinks[conn.RemoteAddr().String()] = link
	this.TcpLinkLastActiveTimes[conn.RemoteAddr().String()] = time.Now().Unix()
	go link.WriteThread()
	if ok {
		oldConn.Close()
	}
	return link
}

// UnregisterTcpLink 注销一个TCP连接。
// 参数：conn - 连接。
func (this *TcpChannelWorker) UnregisterTcpLink(conn net.Conn) {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()
	if oldConn, ok := this.TcpLinks[conn.RemoteAddr().String()]; ok {
		delete(this.TcpLinks, conn.RemoteAddr().String())
		oldConn.Close()
	}
}

// TouchLink 更新连接的活跃时间。
// 参数：conn - 连接。
func (this *TcpChannelWorker) TouchLink(conn net.Conn) {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()
	this.TcpLinkLastActiveTimes[conn.RemoteAddr().String()] = time.Now().Unix()
}

// ReadThread 读取TCP连接数据的主线程。
// 参数：conn - 连接，accepted - 是否为被动接收。
func (this *TcpChannelWorker) ReadThread(conn net.Conn, accepted bool) {
	// 注册link
	link := this.RegisterTcpLink(conn, accepted)
	// 注销link
	defer func() {
		this.UnregisterTcpLink(conn)
		if this.User != nil {
			this.User.onConnectChanged(this, false)
		}
	}()

	head := make([]byte, 4)
	toBeContinueBuffer := []byte{}
	reader := bufio.NewReader(conn)
	for !this.IsClosed {

		this.trySendRegisterMessage(link)

		i := 0
		for i < len(head) {
			Debug("触发读数据帧", "worker=", this.User.getID())
			n, err := reader.Read(head[i:])
			if err != nil {
				return
			}
			i += n
			this.TouchLink(conn)
		}
		length := (uint32(head[1]) << 16) | (uint32(head[2]) << 8) | uint32(head[3])
		ba := make([]byte, int(length))
		i = 0
		for i < len(ba) {
			n, err := reader.Read(ba[i:])
			if err != nil {
				return
			}
			i += n
			this.TouchLink(conn)
		}
		toBeContinueBuffer = append(toBeContinueBuffer, ba...)
		if head[0] == 2 {
			func() {
				defer func() {
					if r := recover(); r != nil {
						Warning("从已关闭通道发送中恢复", "error=", r)
					}
				}()
				this.RecvPackagesChan <- IpPackage{
					RemoteAddr: conn.RemoteAddr().String(),
					Data:       toBeContinueBuffer,
				}
			}()
			toBeContinueBuffer = []byte{}
		}

	}
}

// 尝试发送注册消息。
// 参数：link - TCP连接。
func (this *TcpChannelWorker) trySendRegisterMessage(link *TcpLink) {
	if this.User == nil || link.HasSendRegister {
		return
	}
	Debug("尝试发送注册消息", "worker=", this.User.getID())

	link.HasSendRegister = true

	nodeID := this.User.getID()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := strconv.FormatInt(rand.Int63(), 10)
	dataToSign := nodeID + "|" + timestamp + "|" + nonce
	signature := HMACMD5Sign(dataToSign, this.Token)
	dataField := nodeID + "|" + timestamp + "|" + nonce + "|" + signature

	RegisterMsg := Message{
		MessageData: MessageData{
			Type: TcpRegister,
			Data: dataField,
		},
		From: nodeID,
		To:   "",
	}

	sentItem, err := common.MarshalAny(RegisterMsg)
	if err != nil {
		Error("注册消息序列化失败", "error=", err)
		return
	}

	link.WriteTo(this.wrapToDataFrame(sentItem), true)
}

// WriteTo 向连接写入数据。
// 参数：ba - 数据，important - 是否重要。
// 返回：写入是否成功。
func (this *TcpLink) WriteTo(ba []byte, important bool) (writeOk bool) {
	writeOk = false
	defer func() {
		recover()
		if !writeOk {
			Error("写入数据失败", "data=", ba)
		}
	}()

	// 进入等待channel
	if important {
		this.ImportantWriteChan <- ba
	} else if !this.DropNormalPkgOnQueueFull || len(this.NormalWriteChan) < cap(this.NormalWriteChan) {
		this.NormalWriteChan <- ba
	}
	writeOk = true
	return writeOk
}

// WriteThread 连接写入线程。
func (this *TcpLink) WriteThread() {
	const importantQuota = 5
	importantCount := 0

	for {
		if importantCount < importantQuota {
			select {
			case ba, ok := <-this.ImportantWriteChan:
				if !ok {
					return
				}
				writeAll(this.LinkConn, ba)
				importantCount++
			case ba, ok := <-this.NormalWriteChan:
				if !ok {
					return
				}
				writeAll(this.LinkConn, ba)
				importantCount = 0
			}
		} else {
			select {
			case ba, ok := <-this.NormalWriteChan:
				if !ok {
					return
				}
				writeAll(this.LinkConn, ba)
			case ba, ok := <-this.ImportantWriteChan:
				if !ok {
					return
				}
				writeAll(this.LinkConn, ba)
			}
			importantCount = 0
		}
	}
}

// 写入所有数据到连接。
// 参数：conn - 连接，ba - 数据。
func writeAll(conn net.Conn, ba []byte) {
	total := 0
	for total < len(ba) {
		n, err := conn.Write(ba[total:])
		if err != nil {
			return
		}
		total += n
	}
}

// DeleteInactiveLink 删除不活跃的连接。
// 返回：被删除的连接列表。
func (this *TcpChannelWorker) DeleteInactiveLink() []net.Conn {
	ret := []net.Conn{}
	now := time.Now().Unix()
	inactiveKeys := []string{}

	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()

	// 收集需要删除的连接，包括过长时间没更新或者是过长时间没注册
	for k, v := range this.TcpLinks {
		if lastActiveTime, ok := this.TcpLinkLastActiveTimes[k]; ok {
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
		if link, exists := this.TcpLinks[k]; exists {
			ret = append(ret, link.LinkConn)
			delete(this.TcpLinks, k)
			delete(this.TcpLinkLastActiveTimes, k)
		}
	}

	return ret
}

// ConnectThread 客户端连接线程，主动连接远端。
// 参数：laddr - 本地地址，raddr - 远端地址。
func (this *TcpChannelWorker) ConnectThread(laddr net.Addr, raddr net.Addr) {
	connectFailureCount := 0
	for !this.IsClosed {
		// 查看是否已有与 raddr 建立的连接，若有则直接复用
		this.MutexForTcpLinks.RLock()
		link, ok := this.TcpLinks[raddr.String()]
		this.MutexForTcpLinks.RUnlock()
		var conn net.Conn
		if ok {
			conn = link.LinkConn
		} else {
			// 如果当前 worker 已关闭，直接返回 nil
			if this.IsClosed {
				return
			}

			// 解析远程地址
			remoteAddr, err := net.ResolveTCPAddr("tcp", raddr.String())
			if err != nil {
				conn = nil
			} else {
				// 解析本地地址（失败则设为 nil，让系统分配）
				localAddr, err := net.ResolveTCPAddr("tcp", laddr.String())
				if err != nil {
					localAddr = nil
				}

				// 建立 TCP 连接
				tcpConn, err := net.DialTCP("tcp", localAddr, remoteAddr)
				if err != nil {
					conn = nil
				} else {
					// 如果配置了 TLS，进行 TLS 握手
					if this.TlsConfig != nil {
						tlsConn := tls.Client(tcpConn, this.TlsConfig)
						err = tlsConn.Handshake()
						// tls握手有问题
						if err != nil {
							tlsConn.Close()
							tcpConn.Close()
							conn = nil
							// 连接建立
						} else {
							// 通知上层用户连接已建立
							if this.User != nil {
								this.User.onConnectChanged(this, true)
							}

							// 启动读取线程（TLS 连接）
							go this.ReadThread(tlsConn, false)
							conn = tlsConn
						}
					} else {
						if this.User != nil {
							this.User.onConnectChanged(this, true)
						}

						// 启动读取线程
						go this.ReadThread(tcpConn, false)
						conn = tcpConn
					}
				}
			}
		}

		if conn != nil {
			raddr = conn.RemoteAddr()
			connectFailureCount = 0
		} else {
			if connectFailureCount < 50 {
				connectFailureCount++
			}
		}

		// 指数退避等待时间
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

// StartCleanupThread 启动定时清理线程。
func (this *TcpChannelWorker) StartCleanupThread() {
	for {
		select {
		case <-this.Done:
			return
		case <-time.After(time.Second):
			for _, conn := range this.DeleteInactiveLink() {
				conn.Close()
			}
		}
	}
}

// StartMessageProcessor 启动消息处理线程。
func (this *TcpChannelWorker) StartMessageProcessor() {
	for {
		select {
		case <-this.Done:
			return
		case pkg := <-this.RecvPackagesChan:
			this.processPackage(pkg)
		}
	}
}

// 处理收到的数据包。
// 参数：pkg - IP包。
func (this *TcpChannelWorker) processPackage(pkg IpPackage) {
	pra := pkg.RemoteAddr

	if len(pkg.Data) > 0 {
		var msg Message
		err := common.UnmarshalAny(pkg.Data, &msg)

		if err != nil {
			Error("消息解码失败", "error=", err)
			return
		}
		Debug("收到新消息", "worker=", this.User.getID(), "msg=", msg.Print())

		if msg.Type == TcpRegister {
			this.handleRegisterMessage(pra, &msg)
		} else {
			this.handleNormalMessage(pra, &msg)
		}

	} else {
		//this.handleCheckReturn(pra)
		return
	}
}

// 校验消息哈希。
// 参数：msg - 消息。
// 返回：校验结果。
func (this *TcpChannelWorker) checkHash(msg *Message) bool {
	HashValue := msg.Data
	selfCalculatedHash := HashIDWithToken(msg.From, this.Token)

	result := selfCalculatedHash == HashValue

	// 调试哈希校验
	Debug("哈希校验",
		"用户ID=", this.User.getID(),
		"来源=", msg.From,
		"期望哈希=", selfCalculatedHash,
		"实际哈希=", HashValue,
		"结果=", result)

	return result
}

// 处理注册消息。
// 参数：pra - 地址，msg - 消息。
func (this *TcpChannelWorker) handleRegisterMessage(pra string, msg *Message) {
	if msg.Type != TcpRegister {
		// 其他类型按原逻辑处理
		return
	}
	parts := strings.Split(msg.MessageData.Data, "|")
	if len(parts) != 4 {
		if link, ok := this.TcpLinks[pra]; ok && link != nil {
			link.Close()
		}
		return
	}
	nodeID := parts[0]
	timestamp := parts[1]
	nonce := parts[2]
	signature := parts[3]
	dataToSign := nodeID + "|" + timestamp + "|" + nonce
	if !HMACMD5Verify(dataToSign, this.Token, signature) {
		if link, ok := this.TcpLinks[pra]; ok && link != nil {
			link.Close()
		}
		return
	}

	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()

	if msg.From == this.User.getID() {
		if link, ok := this.TcpLinks[pra]; ok && link != nil {
			link.Close()
		}
		return
	}

	// 检测是否连接以及存储
	if link, ok := this.TcpLinks[pra]; ok && link != nil {
		// 如果有tsl，检测tsl是否正确
		if _, ok := link.TlsServerName[msg.From]; !ok && len(link.TlsServerName) > 0 {
			link.Close()
		} else {
			Debug("收到注册消息", "用户ID=", this.User.getID(), "来源=", msg.From)
			// 连接进行注册
			link.HasRecvRegister = true
		}
	}
}

// 处理普通消息。
// 参数：pra - 地址，msg - 消息。
func (this *TcpChannelWorker) handleNormalMessage(pra string, msg *Message) {
	peerId := msg.LastSender

	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()

	if _, ok := this.LinksAddrFromPeer[peerId]; !ok {
		this.LinksAddrFromPeer[peerId] = []string{}
	}

	match := false
	for i, ra := range this.LinksAddrFromPeer[peerId] {
		if ra == pra {
			match = true

			// 将新触发的link移到最首
			if i != 0 {
				this.LinksAddrFromPeer[peerId][i] = this.LinksAddrFromPeer[peerId][0]
				this.LinksAddrFromPeer[peerId][0] = pra
			}
			break
		}
	}

	// 如果没找到就新增
	if !match {
		this.LinksAddrFromPeer[peerId] = append([]string{pra}, this.LinksAddrFromPeer[peerId]...)
	}

	if user := this.User; user != nil {
		go user.onRecv(peerId, msg)
	}
}

// Close 关闭通道工作者，断开所有连接。
// 返回：错误信息（如有）。
func (this *TcpChannelWorker) Close() error {
	this.IsClosed = true
	close(this.Done)

	if this.Listener != nil {
		this.Listener.Close()
	}

	for _, conn := range this.DeleteInactiveLinkForce() {
		conn.Close()
	}

	close(this.RecvPackagesChan)
	return nil
}

// DeleteInactiveLinkForce 强制删除所有不活跃连接。
// 返回：被删除的连接列表。
func (this *TcpChannelWorker) DeleteInactiveLinkForce() []net.Conn {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()

	ret := []net.Conn{}
	for k, link := range this.TcpLinks {
		ret = append(ret, link.LinkConn)
		delete(this.TcpLinks, k)
		delete(this.TcpLinkLastActiveTimes, k)
	}
	return ret
}

// 发送消息到指定节点。
// 参数：nextHopNodeId - 目标节点ID，msg - 消息。
func (this *TcpChannelWorker) sendTo(nextHopNodeId string, msg *Message) {
	if msg == nil {
		return
	}
	this.MutexForTcpLinks.RLock()

	if ras, ok := this.LinksAddrFromPeer[nextHopNodeId]; ok {
		sentItem, _ := common.MarshalAny(msg)
		ba := this.wrapToDataFrame(sentItem)
		for _, ra := range ras {
			if link, ok := this.TcpLinks[ra]; ok {
				if !link.HasRecvRegister && msg.Type != TcpRegister {
					Debug("消息被阻止发送", "原因=连接未注册", "消息类型=", msg.Type)
					continue
				}
				go func(ba []byte, link *TcpLink, important bool) {
					if len(ba) > 0 {
						Debug("发送消息到目标", "用户ID=", this.User.getID(), "消息=", msg.Print())
						link.WriteTo(ba, important)
					}
				}(ba, link, msg.isImportant())
			}
		}
	}
	this.MutexForTcpLinks.RUnlock()
}

// 广播消息到所有连接。
// 参数：msg - 消息。
func (this *TcpChannelWorker) broadCast(msg *Message) {
	if msg == nil {
		return
	}
	sentItem, _ := common.MarshalAny(msg)
	go func(ba []byte, important bool) {
		if len(ba) > 0 {
			links := this.GetAllConns()
			for _, link := range links {
				if !link.HasRecvRegister && msg.Type != TcpRegister {
					Debug("广播消息被阻止", "原因=连接未注册", "消息类型=", msg.Type)
					continue
				}
				go func(link *TcpLink) {
					Debug("广播消息", "用户ID=", this.User.getID(), "消息=", msg.Print())
					link.WriteTo(ba, important)
				}(link)
			}
		}
	}(this.wrapToDataFrame(sentItem), msg.isImportant())
}

// GetAllConns 获取所有TCP连接。
// 返回：TcpLink切片。
func (this *TcpChannelWorker) GetAllConns() []*TcpLink {
	ret := []*TcpLink{}
	this.MutexForTcpLinks.RLock()
	defer this.MutexForTcpLinks.RUnlock()
	for _, v := range this.TcpLinks {
		ret = append(ret, v)
	}
	return ret
}
