package core

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// SignalingClient 信令客户端
type SignalingClient struct {
	mu             sync.RWMutex
	nodeID         string            // 节点ID
	nodeName       string            // 节点名称
	localPort      int               // 本地端口
	localAddresses []string          // 本地地址列表
	metadata       map[string]string // 节点元数据

	// 信令服务器连接
	signalingURL  string          // 信令服务器地址
	conn          *websocket.Conn // WebSocket连接
	connected     bool            // 连接状态
	reconnectChan chan bool       // 重连信号通道

	// 配置
	config *SignalingClientConfig // 客户端配置

	// 状态管理
	stopChan chan struct{} // 停止信号
	done     chan struct{} // 完成信号

	// UDP公网地址检测
	udpConn *net.UDPConn // UDP连接

	// RPC调用管理
	invokeChannels map[string]chan *KCPConnectionResponse
	invokeMutex    sync.RWMutex

	// Node实例引用
	node *Node // 用于添加通道
}

// SignalingClientConfig 信令客户端配置
type SignalingClientConfig struct {
	SignalingURL      string        `json:"signaling_url"`      // 信令服务器地址
	SignalingUDPPort  int           `json:"signaling_udp_port"` // 信令服务器UDP端口
	ReconnectInterval time.Duration `json:"reconnect_interval"` // 重连间隔
	HeartbeatInterval time.Duration `json:"heartbeat_interval"` // 心跳间隔
	ReadTimeout       time.Duration `json:"read_timeout"`       // 读取超时
	WriteTimeout      time.Duration `json:"write_timeout"`      // 写入超时
	MaxMessageSize    int64         `json:"max_message_size"`   // 最大消息大小
}

// SignalingMessage 信令消息（与服务器端保持一致）
type SignalingMessage struct {
	Type      string                 `json:"type"`      // 消息类型
	From      string                 `json:"from"`      // 发送方节点ID
	To        string                 `json:"to"`        // 接收方节点ID (可选)
	Data      map[string]interface{} `json:"data"`      // 消息数据
	Timestamp time.Time              `json:"timestamp"` // 时间戳
}

// PublicAddressRequest UDP公网地址检测请求
type PublicAddressRequest struct {
	NodeID string `json:"node_id"` // 节点ID
	Port   int    `json:"port"`    // 本地监听端口
}

// PublicAddressResponse UDP公网地址检测响应
type PublicAddressResponse struct {
	NodeID       string `json:"node_id"`       // 节点ID
	LocalPort    int    `json:"local_port"`    // 本地端口
	PublicIP     string `json:"public_ip"`     // 公网IP
	PublicPort   int    `json:"public_port"`   // 公网端口
	PublicAddr   string `json:"public_addr"`   // 完整公网地址
	Success      bool   `json:"success"`       // 是否成功
	ErrorMessage string `json:"error_message"` // 错误信息
}

// KCPConnectionRequest KCP连接请求
type KCPConnectionRequest struct {
	FromNodeID string `json:"from_node_id"` // 发起方节点ID
	ToNodeID   string `json:"to_node_id"`   // 目标节点ID
	InvokeID   string `json:"invoke_id"`    // 调用ID
}

// KCPConnectionResponse KCP连接响应
type KCPConnectionResponse struct {
	InvokeID   string `json:"invoke_id"`    // 调用ID
	FromNodeID string `json:"from_node_id"` // 发起方节点ID
	ToNodeID   string `json:"to_node_id"`   // 目标节点ID
	PublicAddr string `json:"public_addr"`  // KCP Server Channel的公网地址
	Success    bool   `json:"success"`      // 是否成功
	ErrorMsg   string `json:"error_msg"`    // 错误信息
}

// NewSignalingClient 创建新的信令客户端
func NewSignalingClient(nodeID, nodeName string, localPort int, config *SignalingClientConfig) *SignalingClient {
	if config == nil {
		config = &SignalingClientConfig{
			SignalingURL:      "ws://localhost:8080/ws",
			SignalingUDPPort:  8081,
			ReconnectInterval: 5 * time.Second,
			HeartbeatInterval: 30 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      10 * time.Second,
			MaxMessageSize:    512,
		}
	}

	client := &SignalingClient{
		nodeID:         nodeID,
		nodeName:       nodeName,
		localPort:      localPort,
		localAddresses: make([]string, 0),
		metadata:       make(map[string]string),
		signalingURL:   config.SignalingURL,
		connected:      false,
		reconnectChan:  make(chan bool, 1),
		config:         config,
		stopChan:       make(chan struct{}),
		done:           make(chan struct{}),
		invokeChannels: make(map[string]chan *KCPConnectionResponse),
		node:           nil,
	}

	// 获取本地地址
	client.discoverLocalAddresses()

	return client
}

// SetNode 设置节点实例引用
func (c *SignalingClient) SetNode(node *Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.node = node
}

// findAvailablePort 查找可用端口
func (c *SignalingClient) findAvailablePort() (int, error) {
	// 从localPort开始尝试，如果被占用就递增
	for port := c.localPort; port < c.localPort+100; port++ {
		addr := fmt.Sprintf(":%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found in range %d-%d", c.localPort, c.localPort+100)
}

// discoverLocalAddresses 发现本地地址
func (c *SignalingClient) discoverLocalAddresses() {
	interfaces, err := net.Interfaces()
	if err != nil {
		Error("获取网络接口失败", "错误=", err)
		return
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				if ipnet.IP.To4() != nil {
					ip := ipnet.IP.String()
					if !isPrivateIP(ip) {
						c.localAddresses = append(c.localAddresses, ip)
					}
				}
			}
		}
	}

	if len(c.localAddresses) == 0 {
		c.localAddresses = append(c.localAddresses, "127.0.0.1")
	}

	Log("发现本地地址", "地址列表=", c.localAddresses)
}

// isPrivateIP 判断是否为私有IP地址
func isPrivateIP(ip string) bool {
	privateRanges := []string{
		"10.", "192.168.", "172.16.", "172.17.", "172.18.", "172.19.",
		"172.20.", "172.21.", "172.22.", "172.23.", "172.24.", "172.25.",
		"172.26.", "172.27.", "172.28.", "172.29.", "172.30.", "172.31.",
	}

	for _, prefix := range privateRanges {
		if len(ip) >= len(prefix) && ip[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Connect 连接到信令服务器
func (c *SignalingClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return fmt.Errorf("already connected")
	}

	u, err := url.Parse(c.signalingURL)
	if err != nil {
		return fmt.Errorf("invalid signaling URL: %v", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to connect to signaling server: %v", err)
	}

	c.conn = conn
	c.connected = true

	conn.SetReadLimit(c.config.MaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout))
		return nil
	})

	go c.handleMessages()
	go c.startHeartbeat()
	go c.monitorConnection()

	return c.register()
}

// register 注册到信令服务器
func (c *SignalingClient) register() error {
	msg := &SignalingMessage{
		Type: "register",
		From: c.nodeID,
		Data: map[string]interface{}{
			"node_id": c.nodeID,
			"name":    c.nodeName,
		},
		Timestamp: time.Now(),
	}

	return c.sendMessage(msg)
}

// handleMessages 处理接收到的消息
func (c *SignalingClient) handleMessages() {
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		close(c.done)
	}()

	for {
		select {
		case <-c.stopChan:
			return
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					Error("WebSocket读取错误", "错误=", err)
				}
				select {
				case c.reconnectChan <- true:
				default:
				}
				return
			}

			var msg SignalingMessage
			if err := json.Unmarshal(message, &msg); err != nil {
				Error("消息反序列化失败", "错误=", err)
				continue
			}

			if err := c.processMessage(&msg); err != nil {
				Error("消息处理失败", "错误=", err)
			}
		}
	}
}

// processMessage 处理消息
func (c *SignalingClient) processMessage(msg *SignalingMessage) error {
	switch msg.Type {
	case "register_response":
		return c.handleRegisterResponse(msg)
	case "heartbeat_response":
		return c.handleHeartbeatResponse(msg)
	case "request_kcp_connection":
		return c.handleRequestKCPConnection(msg)
	case "kcp_connection_response":
		return c.handleKCPConnectionResponse(msg)
	default:
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// sendMessage 发送消息
func (c *SignalingClient) sendMessage(msg *SignalingMessage) error {
	c.mu.RLock()
	if !c.connected || c.conn == nil {
		c.mu.RUnlock()
		return fmt.Errorf("not connected to signaling server")
	}
	conn := c.conn
	c.mu.RUnlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// startHeartbeat 启动心跳
func (c *SignalingClient) startHeartbeat() {
	ticker := time.NewTicker(c.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			msg := &SignalingMessage{
				Type:      "heartbeat",
				From:      c.nodeID,
				Data:      map[string]interface{}{"timestamp": time.Now().Unix()},
				Timestamp: time.Now(),
			}
			if err := c.sendMessage(msg); err != nil {
				Error("心跳发送失败", "错误=", err)
			}
		case <-c.stopChan:
			return
		}
	}
}

// monitorConnection 监控连接状态
func (c *SignalingClient) monitorConnection() {
	for {
		select {
		case <-c.reconnectChan:
			Log("尝试重连信令服务器")
			c.reconnect()
		case <-c.stopChan:
			return
		}
	}
}

// reconnect 重连
func (c *SignalingClient) reconnect() {
	for {
		select {
		case <-c.stopChan:
			return
		default:
			time.Sleep(c.config.ReconnectInterval)

			if err := c.Connect(); err == nil {
				Log("成功重连信令服务器")
				return
			} else {
				Error("重连失败", "错误=", err)
			}
		}
	}
}

// Disconnect 断开连接
func (c *SignalingClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil
	}

	msg := &SignalingMessage{
		Type:      "disconnect",
		From:      c.nodeID,
		Timestamp: time.Now(),
	}
	c.sendMessage(msg)

	if c.conn != nil {
		c.conn.Close()
	}

	c.connected = false
	close(c.stopChan)

	return nil
}

// RequestKCPConnection 请求与目标节点建立KCP连接
func (c *SignalingClient) RequestKCPConnection(toNodeID string) (*KCPConnectionResponse, error) {
	// 生成调用ID
	invokeID := fmt.Sprintf("%s-%s-%d", c.nodeID, toNodeID, time.Now().UnixNano())

	// 创建响应通道
	responseChan := make(chan *KCPConnectionResponse, 1)

	// 注册调用ID
	c.invokeMutex.Lock()
	c.invokeChannels[invokeID] = responseChan
	c.invokeMutex.Unlock()

	// 清理函数
	defer func() {
		c.invokeMutex.Lock()
		delete(c.invokeChannels, invokeID)
		c.invokeMutex.Unlock()
	}()

	// 发送连接请求
	msg := &SignalingMessage{
		Type: "request_kcp_connection",
		From: c.nodeID,
		Data: map[string]interface{}{
			"to_node_id": toNodeID,
			"invoke_id":  invokeID,
		},
		Timestamp: time.Now(),
	}

	if err := c.sendMessage(msg); err != nil {
		return nil, fmt.Errorf("failed to send KCP connection request: %v", err)
	}

	Log("发送KCP连接请求", "目标节点=", toNodeID, "调用ID=", invokeID)

	// 等待响应
	select {
	case response := <-responseChan:
		if response.Success {
			// 创建KCP客户端通道连接到目标节点
			clientWorker := NewKcpConnection(
				toNodeID,
				true,                // 是客户端
				"",                  // 本地监听地址为空（因为是客户端）
				response.PublicAddr, // 目标地址
				nil,
				4096,
				10,
				1000,
				5,
				"default_token",
			)

			if clientWorker == nil {
				Error("创建KCP客户端工作器失败")
				response.Success = false
				response.ErrorMsg = "Failed to create KCP client worker"
			} else {
				// 创建通道并添加到节点
				c.mu.RLock()
				node := c.node
				c.mu.RUnlock()

				if node == nil {
					Error("节点实例未设置")
					response.Success = false
					response.ErrorMsg = "Node instance not set"
				} else {
					// 创建通道
					channelID := fmt.Sprintf("%s-to-%s", c.nodeID, toNodeID)
					channel := NewChannelFull(channelID, node, clientWorker)
					clientWorker.User = channel

					// 添加到节点
					node.AddChannel(channel)
					Log("成功创建KCP客户端通道", "目标节点=", toNodeID, "公网地址=", response.PublicAddr)
				}
			}
		}
		return response, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("KCP connection request timeout")
	}
}

// DetectPublicAddress UDP公网地址检测
func (c *SignalingClient) DetectPublicAddress(localPort int) (*PublicAddressResponse, error) {
	// 解析信令服务器地址
	u, err := url.Parse(c.signalingURL)
	if err != nil {
		return nil, fmt.Errorf("invalid signaling URL: %v", err)
	}

	// 构建UDP目标地址
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", u.Hostname(), c.config.SignalingUDPPort))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve UDP address: %v", err)
	}

	// 创建本地UDP连接
	localAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", localPort))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve local UDP address: %v", err)
	}

	udpConn, err := net.DialUDP("udp", localAddr, udpAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to create UDP connection: %v", err)
	}
	defer udpConn.Close()

	// 构建请求
	request := PublicAddressRequest{
		NodeID: c.nodeID,
		Port:   localPort,
	}

	requestData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	// 发送UDP请求
	_, err = udpConn.Write(requestData)
	if err != nil {
		return nil, fmt.Errorf("failed to send UDP request: %v", err)
	}

	Log("发送UDP公网地址检测请求", "本地端口=", localPort)

	// 设置读取超时
	udpConn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// 读取响应
	buffer := make([]byte, 1024)
	n, _, err := udpConn.ReadFromUDP(buffer)
	if err != nil {
		return nil, fmt.Errorf("failed to read UDP response: %v", err)
	}

	// 解析响应
	var response PublicAddressResponse
	if err := json.Unmarshal(buffer[:n], &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal UDP response: %v", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("public address detection failed: %s", response.ErrorMessage)
	}

	Log("检测到公网地址", "公网地址=", response.PublicAddr, "本地端口=", localPort)
	return &response, nil
}

// 消息处理器实现

// handleRegisterResponse 处理注册响应
func (c *SignalingClient) handleRegisterResponse(msg *SignalingMessage) error {
	Log("成功注册到信令服务器")
	return nil
}

// handleHeartbeatResponse 处理心跳响应
func (c *SignalingClient) handleHeartbeatResponse(msg *SignalingMessage) error {
	return nil
}

// handleRequestKCPConnection 处理KCP连接请求（作为目标节点）
func (c *SignalingClient) handleRequestKCPConnection(msg *SignalingMessage) error {
	fromNodeID := msg.From
	invokeID, ok := msg.Data["invoke_id"].(string)
	if !ok {
		return fmt.Errorf("missing invoke_id in request_kcp_connection message")
	}

	Log("收到KCP连接请求", "来源节点=", fromNodeID, "调用ID=", invokeID)

	// 1. 选择一个可用的本地端口
	localPort, err := c.findAvailablePort()
	if err != nil {
		Error("查找可用端口失败", "错误=", err)
		responseMsg := &SignalingMessage{
			Type: "kcp_connection_response",
			From: c.nodeID,
			Data: map[string]interface{}{
				"invoke_id":    invokeID,
				"from_node_id": fromNodeID,
				"to_node_id":   c.nodeID,
				"success":      false,
				"error_msg":    fmt.Sprintf("Failed to find available port: %v", err),
			},
			Timestamp: time.Now(),
		}
		return c.sendMessage(responseMsg)
	}

	// 2. 通过UDP检测公网地址
	publicAddrResponse, err := c.DetectPublicAddress(localPort)
	if err != nil {
		Error("检测公网地址失败", "错误=", err)
		// 发送错误响应
		responseMsg := &SignalingMessage{
			Type: "kcp_connection_response",
			From: c.nodeID,
			Data: map[string]interface{}{
				"invoke_id":    invokeID,
				"from_node_id": fromNodeID,
				"to_node_id":   c.nodeID,
				"success":      false,
				"error_msg":    fmt.Sprintf("Failed to detect public address: %v", err),
			},
			Timestamp: time.Now(),
		}
		return c.sendMessage(responseMsg)
	}

	Log("检测到公网地址", "公网地址=", publicAddrResponse.PublicAddr, "本地端口=", localPort)

	// 3. 创建KCP服务器通道
	serverWorker := NewKcpConnection(
		fromNodeID,
		false,                         // 不是客户端
		fmt.Sprintf(":%d", localPort), // 监听地址
		"",                            // 目标地址为空（因为是服务器）
		nil,
		4096,
		10,
		1000,
		5,
		"default_token",
	)

	if serverWorker == nil {
		Error("创建KCP服务器工作器失败")
		responseMsg := &SignalingMessage{
			Type: "kcp_connection_response",
			From: c.nodeID,
			Data: map[string]interface{}{
				"invoke_id":    invokeID,
				"from_node_id": fromNodeID,
				"to_node_id":   c.nodeID,
				"success":      false,
				"error_msg":    "Failed to create KCP server worker",
			},
			Timestamp: time.Now(),
		}
		return c.sendMessage(responseMsg)
	}

	// 4. 创建通道并添加到节点
	c.mu.RLock()
	node := c.node
	c.mu.RUnlock()

	if node == nil {
		Error("节点实例未设置")
		responseMsg := &SignalingMessage{
			Type: "kcp_connection_response",
			From: c.nodeID,
			Data: map[string]interface{}{
				"invoke_id":    invokeID,
				"from_node_id": fromNodeID,
				"to_node_id":   c.nodeID,
				"success":      false,
				"error_msg":    "Node instance not set",
			},
			Timestamp: time.Now(),
		}
		return c.sendMessage(responseMsg)
	}

	// 创建通道
	channelID := fmt.Sprintf("%s-from-%s", c.nodeID, fromNodeID)
	channel := NewChannelFull(channelID, node, serverWorker)
	serverWorker.User = channel

	// 添加到节点
	node.AddChannel(channel)

	// 5. 发送成功响应
	responseMsg := &SignalingMessage{
		Type: "kcp_connection_response",
		From: c.nodeID,
		Data: map[string]interface{}{
			"invoke_id":    invokeID,
			"from_node_id": fromNodeID,
			"to_node_id":   c.nodeID,
			"public_addr":  publicAddrResponse.PublicAddr,
			"success":      true,
			"error_msg":    "",
		},
		Timestamp: time.Now(),
	}

	Log("成功创建KCP服务器通道", "来源节点=", fromNodeID, "端口=", localPort)
	return c.sendMessage(responseMsg)
}

// handleKCPConnectionResponse 处理KCP连接响应（作为发起方）
func (c *SignalingClient) handleKCPConnectionResponse(msg *SignalingMessage) error {
	invokeID, ok := msg.Data["invoke_id"].(string)
	if !ok {
		return fmt.Errorf("missing invoke_id in kcp_connection_response message")
	}

	// 查找对应的通道
	c.invokeMutex.RLock()
	responseChan, exists := c.invokeChannels[invokeID]
	c.invokeMutex.RUnlock()

	if !exists {
		return fmt.Errorf("invoke_id not found: %s", invokeID)
	}

	// 构建响应对象
	response := &KCPConnectionResponse{
		InvokeID:   invokeID,
		FromNodeID: msg.Data["from_node_id"].(string),
		ToNodeID:   msg.Data["to_node_id"].(string),
		PublicAddr: msg.Data["public_addr"].(string),
		Success:    msg.Data["success"].(bool),
	}

	if errorMsg, ok := msg.Data["error_msg"].(string); ok {
		response.ErrorMsg = errorMsg
	}

	// 发送响应到通道
	select {
	case responseChan <- response:
		Log("发送KCP连接响应到通道", "调用ID=", invokeID)
	default:
		Error("通道已满", "调用ID=", invokeID)
	}

	return nil
}

// IsConnected 检查是否连接到信令服务器
func (c *SignalingClient) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// GetNodeInfo 获取节点信息
func (c *SignalingClient) GetNodeInfo() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return map[string]interface{}{
		"node_id":         c.nodeID,
		"node_name":       c.nodeName,
		"local_port":      c.localPort,
		"local_addresses": c.localAddresses,
		"metadata":        c.metadata,
		"connected":       c.connected,
	}
}

// GetLocalAddresses 获取本地地址列表
func (c *SignalingClient) GetLocalAddresses() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]string, len(c.localAddresses))
	copy(result, c.localAddresses)
	return result
}

// GetLocalPort 获取本地端口
func (c *SignalingClient) GetLocalPort() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.localPort
}
