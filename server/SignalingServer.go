package server

import (
	"Majula/core"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// NodeInfo 简化的节点信息
type NodeInfo struct {
	ID       string    `json:"id"`        // 节点唯一标识
	Name     string    `json:"name"`      // 节点名称
	Status   string    `json:"status"`    // 节点状态：在线、离线
	LastSeen time.Time `json:"last_seen"` // 最后活跃时间
}

// SignalingMessage 信令消息
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

// SignalingServer 信令服务器
type SignalingServer struct {
	mu          sync.RWMutex
	nodes       map[string]*NodeInfo       // 节点信息映射
	connections map[string]*websocket.Conn // WebSocket连接映射
	upgrader    websocket.Upgrader         // WebSocket升级器
	config      *SignalingConfig           // 服务器配置
	router      *gin.Engine                // Gin路由器

	// UDP公网地址检测
	udpListener *net.UDPConn // UDP监听器
	udpPort     int          // UDP监听端口

	// 清理定时器
	cleanupTicker *time.Ticker
}

// SignalingConfig 信令服务器配置
type SignalingConfig struct {
	Port            int           `json:"port"`             // 监听端口
	UDPPort         int           `json:"udp_port"`         // UDP监听端口
	ReadTimeout     time.Duration `json:"read_timeout"`     // 读取超时
	WriteTimeout    time.Duration `json:"write_timeout"`    // 写入超时
	PingInterval    time.Duration `json:"ping_interval"`    // 心跳间隔
	PongWait        time.Duration `json:"pong_wait"`        // Pong等待时间
	MaxMessageSize  int64         `json:"max_message_size"` // 最大消息大小
	CleanupInterval time.Duration `json:"cleanup_interval"` // 清理间隔
}

// NewSignalingServer 创建新的信令服务器
func NewSignalingServer(config *SignalingConfig) *SignalingServer {
	if config == nil {
		config = &SignalingConfig{
			Port:            8080,
			UDPPort:         8081,
			ReadTimeout:     60 * time.Second,
			WriteTimeout:    10 * time.Second,
			PingInterval:    54 * time.Second,
			PongWait:        60 * time.Second,
			MaxMessageSize:  512,
			CleanupInterval: 30 * time.Second,
		}
	}

	server := &SignalingServer{
		nodes:       make(map[string]*NodeInfo),
		connections: make(map[string]*websocket.Conn),
		config:      config,
		router:      gin.Default(),
		udpPort:     config.UDPPort,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}

	// 设置Gin路由
	server.setupRoutes()

	return server
}

// setupRoutes 设置Gin路由
func (s *SignalingServer) setupRoutes() {
	// WebSocket路由
	s.router.GET("/ws", s.handleWebSocketGin)

	// API路由组
	api := s.router.Group("/api")
	{
		api.GET("/nodes", s.handleGetNodesGin)
		api.GET("/health", s.handleHealthGin)
	}

	// 添加中间件
	s.router.Use(gin.Logger())
	s.router.Use(gin.Recovery())
}

// Start 启动信令服务器
func (s *SignalingServer) Start() error {
	// 启动UDP监听器
	if err := s.startUDPListener(); err != nil {
		return fmt.Errorf("failed to start UDP listener: %v", err)
	}

	// 启动清理定时器
	s.startCleanupTimer()

	addr := fmt.Sprintf(":%d", s.config.Port)
	core.Log("信令服务器启动", "地址=", addr, "UDP端口=", s.udpPort)
	return s.router.Run(addr)
}

// startUDPListener 启动UDP监听器
func (s *SignalingServer) startUDPListener() error {
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", s.udpPort))
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %v", err)
	}

	s.udpListener, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to start UDP listener: %v", err)
	}

	core.Log("UDP监听器启动", "端口=", s.udpPort)

	// 启动UDP消息处理协程
	go s.handleUDPMessages()

	return nil
}

// startCleanupTimer 启动清理定时器
func (s *SignalingServer) startCleanupTimer() {
	s.cleanupTicker = time.NewTicker(s.config.CleanupInterval)
	go func() {
		for range s.cleanupTicker.C {
			s.cleanupOfflineNodes()
		}
	}()
}

// cleanupOfflineNodes 清理离线节点
func (s *SignalingServer) cleanupOfflineNodes() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for nodeID, node := range s.nodes {
		if node.Status == "offline" && now.Sub(node.LastSeen) > 5*time.Minute {
			delete(s.nodes, nodeID)
			core.Log("清理离线节点", "节点ID=", nodeID)
		}
	}
}

// handleUDPMessages 处理UDP消息
func (s *SignalingServer) handleUDPMessages() {
	buffer := make([]byte, 1024)

	for {
		n, remoteAddr, err := s.udpListener.ReadFromUDP(buffer)
		if err != nil {
			core.Error("UDP读取错误", "错误=", err)
			continue
		}

		message := string(buffer[:n])
		core.Log("收到UDP消息", "来源地址=", remoteAddr.String(), "消息=", message)

		// 解析消息
		var request PublicAddressRequest
		if err := json.Unmarshal(buffer[:n], &request); err != nil {
			core.Error("UDP消息反序列化失败", "错误=", err)
			continue
		}

		// 处理公网地址检测请求
		go s.handleUDPPublicAddressRequest(request, remoteAddr)
	}
}

// handleUDPPublicAddressRequest 处理UDP公网地址检测请求
func (s *SignalingServer) handleUDPPublicAddressRequest(request PublicAddressRequest, remoteAddr *net.UDPAddr) {
	core.Log("处理UDP公网地址请求", "节点ID=", request.NodeID, "来源地址=", remoteAddr.String())

	// 提取公网地址
	publicIP := remoteAddr.IP.String()
	publicPort := remoteAddr.Port

	// 构建响应
	response := PublicAddressResponse{
		NodeID:     request.NodeID,
		LocalPort:  request.Port,
		PublicIP:   publicIP,
		PublicPort: publicPort,
		PublicAddr: fmt.Sprintf("%s:%d", publicIP, publicPort),
		Success:    true,
	}

	// 发送响应
	responseData, err := json.Marshal(response)
	if err != nil {
		core.Error("UDP响应序列化失败", "错误=", err)
		return
	}

	_, err = s.udpListener.WriteToUDP(responseData, remoteAddr)
	if err != nil {
		core.Error("UDP响应发送失败", "错误=", err)
		return
	}

	core.Log("发送UDP响应", "目标地址=", remoteAddr.String(), "公网地址=", response.PublicAddr)
}

// handleWebSocketGin 处理WebSocket连接
func (s *SignalingServer) handleWebSocketGin(c *gin.Context) {
	conn, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		core.Error("WebSocket升级失败", "错误=", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WebSocket upgrade failed"})
		return
	}

	// 设置连接参数
	conn.SetReadLimit(s.config.MaxMessageSize)
	conn.SetReadDeadline(time.Now().Add(s.config.PongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(s.config.PongWait))
		return nil
	})

	// 启动消息处理协程
	go s.handleConnection(conn)
}

// handleConnection 处理单个WebSocket连接
func (s *SignalingServer) handleConnection(conn *websocket.Conn) {
	defer func() {
		conn.Close()
		s.cleanupDisconnectedConnections(conn)
	}()

	// 启动心跳协程
	go s.startHeartbeat(conn)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				core.Error("WebSocket读取错误", "错误=", err)
			}
			break
		}

		// 解析消息
		var msg SignalingMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			core.Error("消息反序列化失败", "错误=", err)
			continue
		}

		// 处理消息
		if err := s.processMessage(conn, &msg); err != nil {
			core.Error("消息处理失败", "错误=", err)
		}
	}
}

// processMessage 处理消息
func (s *SignalingServer) processMessage(conn *websocket.Conn, msg *SignalingMessage) error {
	switch msg.Type {
	case "register":
		return s.handleRegister(conn, msg)
	case "heartbeat":
		return s.handleHeartbeat(conn, msg)
	case "disconnect":
		return s.handleDisconnect(conn, msg)
	case "request_kcp_connection":
		return s.handleRequestKCPConnection(conn, msg)
	case "kcp_connection_response":
		return s.handleKCPConnectionResponse(conn, msg)
	default:
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// handleRegister 处理节点注册
func (s *SignalingServer) handleRegister(conn *websocket.Conn, msg *SignalingMessage) error {
	nodeID, ok := msg.Data["node_id"].(string)
	if !ok {
		return fmt.Errorf("missing node_id in register message")
	}

	name, _ := msg.Data["name"].(string)

	node := &NodeInfo{
		ID:       nodeID,
		Name:     name,
		Status:   "online",
		LastSeen: time.Now(),
	}

	s.mu.Lock()
	s.nodes[nodeID] = node
	s.connections[nodeID] = conn
	s.mu.Unlock()

	core.Log("节点已注册", "节点ID=", nodeID, "节点名称=", name)

	// 发送注册成功响应
	response := &SignalingMessage{
		Type:      "register_response",
		From:      "server",
		To:        nodeID,
		Data:      map[string]interface{}{"status": "success"},
		Timestamp: time.Now(),
	}

	return s.sendMessage(conn, response)
}

// handleHeartbeat 处理心跳消息
func (s *SignalingServer) handleHeartbeat(conn *websocket.Conn, msg *SignalingMessage) error {
	nodeID := msg.From

	s.mu.Lock()
	if node, exists := s.nodes[nodeID]; exists {
		node.LastSeen = time.Now()
		node.Status = "online"
	}
	s.mu.Unlock()

	// 发送心跳响应
	response := &SignalingMessage{
		Type:      "heartbeat_response",
		From:      "server",
		To:        nodeID,
		Data:      map[string]interface{}{"timestamp": time.Now().Unix()},
		Timestamp: time.Now(),
	}

	return s.sendMessage(conn, response)
}

// handleRequestKCPConnection 处理KCP连接请求
func (s *SignalingServer) handleRequestKCPConnection(conn *websocket.Conn, msg *SignalingMessage) error {
	fromNodeID := msg.From
	toNodeID, ok := msg.Data["to_node_id"].(string)
	if !ok {
		return fmt.Errorf("missing to_node_id in request_kcp_connection message")
	}

	invokeID, ok := msg.Data["invoke_id"].(string)
	if !ok {
		return fmt.Errorf("missing invoke_id in request_kcp_connection message")
	}

	core.Log("处理KCP连接请求", "来源节点=", fromNodeID, "目标节点=", toNodeID, "调用ID=", invokeID)

	// 检查目标节点是否在线
	s.mu.RLock()
	targetConn, exists := s.connections[toNodeID]
	s.mu.RUnlock()

	if !exists {
		// 目标节点不在线，发送错误响应
		response := &SignalingMessage{
			Type:      "kcp_connection_response",
			From:      "server",
			To:        fromNodeID,
			Data:      map[string]interface{}{"error": "target node not found", "invoke_id": invokeID},
			Timestamp: time.Now(),
		}
		return s.sendMessage(conn, response)
	}

	// 转发连接请求给目标节点
	request := &SignalingMessage{
		Type: "request_kcp_connection",
		From: fromNodeID,
		To:   toNodeID,
		Data: map[string]interface{}{
			"from_node_id": fromNodeID,
			"to_node_id":   toNodeID,
			"invoke_id":    invokeID,
		},
		Timestamp: time.Now(),
	}

	return s.sendMessage(targetConn, request)
}

// handleKCPConnectionResponse 处理KCP连接响应
func (s *SignalingServer) handleKCPConnectionResponse(conn *websocket.Conn, msg *SignalingMessage) error {
	fromNodeID := msg.From
	invokeID, ok := msg.Data["invoke_id"].(string)
	if !ok {
		return fmt.Errorf("missing invoke_id in kcp_connection_response message")
	}

	// 查找发起方连接
	s.mu.RLock()
	initiatorConn, exists := s.connections[fromNodeID]
	s.mu.RUnlock()

	if !exists {
		core.Error("发起方节点未找到", "节点ID=", fromNodeID, "调用ID=", invokeID)
		return nil
	}

	// 转发响应给发起方
	response := &SignalingMessage{
		Type:      "kcp_connection_response",
		From:      fromNodeID,
		To:        fromNodeID,
		Data:      msg.Data,
		Timestamp: time.Now(),
	}

	core.Log("转发KCP连接响应", "调用ID=", invokeID, "来源节点=", fromNodeID)

	return s.sendMessage(initiatorConn, response)
}

// handleDisconnect 处理断开连接
func (s *SignalingServer) handleDisconnect(conn *websocket.Conn, msg *SignalingMessage) error {
	nodeID := msg.From

	s.mu.Lock()
	if node, exists := s.nodes[nodeID]; exists {
		node.Status = "offline"
		node.LastSeen = time.Now()
	}
	delete(s.connections, nodeID)
	s.mu.Unlock()

	core.Log("节点断开连接", "节点ID=", nodeID)
	return nil
}

// sendMessage 发送消息
func (s *SignalingServer) sendMessage(conn *websocket.Conn, msg *SignalingMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}

	conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, data)
}

// startHeartbeat 启动心跳
func (s *SignalingServer) startHeartbeat(conn *websocket.Conn) {
	ticker := time.NewTicker(s.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// cleanupDisconnectedConnections 清理断开的连接
func (s *SignalingServer) cleanupDisconnectedConnections(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for nodeID, nodeConn := range s.connections {
		if nodeConn == conn {
			if node, exists := s.nodes[nodeID]; exists {
				node.Status = "offline"
				node.LastSeen = time.Now()
			}
			delete(s.connections, nodeID)
			core.Log("清理断开连接的节点", "节点ID=", nodeID)
			break
		}
	}
}

// Gin HTTP处理器

// handleGetNodesGin 获取所有节点信息
func (s *SignalingServer) handleGetNodesGin(c *gin.Context) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var nodes []*NodeInfo
	for _, node := range s.nodes {
		nodes = append(nodes, node)
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes": nodes,
		"count": len(nodes),
	})
}

// handleHealthGin 健康检查
func (s *SignalingServer) handleHealthGin(c *gin.Context) {
	s.mu.RLock()
	onlineCount := 0
	for _, node := range s.nodes {
		if node.Status == "online" {
			onlineCount++
		}
	}
	s.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"status":       "healthy",
		"total_nodes":  len(s.nodes),
		"online_nodes": onlineCount,
		"timestamp":    time.Now().Unix(),
	})
}
