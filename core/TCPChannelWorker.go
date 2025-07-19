package core

import (
	"Majula/common"
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
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

func (CWorker *TcpChannelWorker) getID() string {
	return CWorker.Name
}

func NewTcpConnection(name string, isClient bool, localAddr string, remoteAddr string,
	ipWhitelist []string, maxFrameSize int,
	maxInactiveDlt int64, maxSendQueueSize int, maxConnectionPerSeconds int, tlsConfig *tls.Config, pToken string) *TcpChannelWorker {

	localTcpaddr, err := net.ResolveTCPAddr("tcp", localAddr)
	if err != nil {
		if !isClient {
			//fmt.Println("Error resolving local address", localAddr, ":", err)
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
				//fmt.Println("Error listening on", localTcpaddr, ":", err)
				return nil
			}
		} else {
			listener, err = tls.Listen("tcp", localTcpaddr.String(), tlsConfig)
			if err != nil {
				//fmt.Println("Error listening on (with tls) ", localTcpaddr, ":", err)
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
			//fmt.Println("Error resolving remote address", remoteAddr, ":", err)
			return nil
		}
		go ret.ConnectThread(localTcpaddr, remoteTcpaddr)
	}

	// 开启控制线程
	go ret.StartCleanupThread()
	go ret.StartMessageProcessor()
	return ret

}

func (this *TcpChannelWorker) AcceptThread() {
	var acceptlimiter = rate.NewLimiter(rate.Limit(this.MaxConnectionPerSecond), this.MaxConnectionPerSecond*2) // 每秒连接控制

	for !this.IsClosed {
		conn, err := this.Listener.Accept()
		if err != nil {
			//fmt.Println("Error accepting connection:", err)
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

func (this *TcpChannelWorker) UnregisterTcpLink(conn net.Conn) {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()
	if oldConn, ok := this.TcpLinks[conn.RemoteAddr().String()]; ok {
		delete(this.TcpLinks, conn.RemoteAddr().String())
		oldConn.Close()
	}
}

func (this *TcpChannelWorker) TouchLink(conn net.Conn) {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()
	this.TcpLinkLastActiveTimes[conn.RemoteAddr().String()] = time.Now().Unix()
}
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
			//fmt.Println(this.User.getID() + "触发了读数据帧")
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
						//fmt.Println("Recovered from send on closed channel:", r)
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

func (this *TcpChannelWorker) trySendRegisterMessage(link *TcpLink) {
	if this.User == nil || link.HasSendRegister {
		return
	}
	fmt.Println(this.User.getID() + " try to send register message")

	link.HasSendRegister = true

	RegisterMsg := Message{
		MessageData: MessageData{
			Type: TcpRegister,
			Data: HashIDWithToken(this.User.getID(), this.Token),
		},
		From: this.User.getID(),
		To:   "",
	}

	sentItem, err := json.Marshal(RegisterMsg)
	if err != nil {
		fmt.Println("Error marshaling register message:", err)
		return
	}

	link.WriteTo(this.wrapToDataFrame(sentItem), true)
}

func (this *TcpLink) WriteTo(ba []byte, important bool) (writeOk bool) {
	writeOk = false
	defer func() {
		recover()
		if !writeOk {
			//fmt.Println("Error writing to", ba)
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

// 实际函数将数据流写入连接
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

func (this *TcpChannelWorker) processPackage(pkg IpPackage) {
	pra := pkg.RemoteAddr

	if len(pkg.Data) > 0 {
		var msg Message
		err := json.Unmarshal(pkg.Data, &msg)

		if err != nil {
			//fmt.Printf("failed to decode message: %v", err)
			return
		}
		//fmt.Println(this.User.getID() + "收到了新的信息")
		//fmt.Println(msg.Print())

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

func (this *TcpChannelWorker) checkHash(msg *Message) bool {
	HashValue := msg.Data
	selfCalculatedHash := HashIDWithToken(msg.From, this.Token)

	result := selfCalculatedHash == HashValue

	// Debug print
	/*
		fmt.Printf(
			"checkHash | UserID: %v | From: %v | ExpectedHash: %v | ActualHash: %v | Result: %v\n",
			this.User.getID(),
			msg.From,
			selfCalculatedHash,
			HashValue,
			result,
		)

	*/

	return result
}
func (this *TcpChannelWorker) handleRegisterMessage(pra string, msg *Message) {
	this.MutexForTcpLinks.Lock()
	defer this.MutexForTcpLinks.Unlock()

	// 检查msg的hash以及来源
	if this.checkHash(msg) != true || msg.From == this.User.getID() {
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
			//fmt.Println(this.User.getID() + " 收到了来自" + msg.From + " 的link的注册消息")
			//连接进行注册
			link.HasRecvRegister = true
		}
	}
}

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

func (this *TcpChannelWorker) sendTo(nextHopNodeId string, msg *Message) {
	if msg == nil {
		return
	}
	this.MutexForTcpLinks.RLock()

	if ras, ok := this.LinksAddrFromPeer[nextHopNodeId]; ok {
		sentItem, _ := json.Marshal(msg)
		ba := this.wrapToDataFrame(sentItem)
		for _, ra := range ras {
			if link, ok := this.TcpLinks[ra]; ok {
				if !link.HasRecvRegister && msg.Type != TcpRegister {
					//fmt.Println("The msg is blocked!")
					continue
				}
				go func(ba []byte, link *TcpLink, important bool) {
					if len(ba) > 0 {
						//fmt.Println(this.User.getID() + " Write in the sendTo " + msg.Print())
						link.WriteTo(ba, important)
					}
				}(ba, link, msg.isImportant())
			}
		}
	}
	this.MutexForTcpLinks.RUnlock()
}

func (this *TcpChannelWorker) broadCast(msg *Message) {
	if msg == nil {
		return
	}
	sentItem, _ := json.Marshal(msg)
	go func(ba []byte, important bool) {
		if len(ba) > 0 {
			links := this.GetAllConns()
			for _, link := range links {
				if !link.HasRecvRegister && msg.Type != TcpRegister {
					//fmt.Println("The msg is blocked!")
					continue
				}
				go func(link *TcpLink) {
					//fmt.Println(this.User.getID() + " Write in the broadcast " + msg.Print())
					link.WriteTo(ba, important)
				}(link)
			}
		}
	}(this.wrapToDataFrame(sentItem), msg.isImportant())
}

func (this *TcpChannelWorker) GetAllConns() []*TcpLink {
	ret := []*TcpLink{}
	this.MutexForTcpLinks.RLock()
	defer this.MutexForTcpLinks.RUnlock()
	for _, v := range this.TcpLinks {
		ret = append(ret, v)
	}
	return ret
}
