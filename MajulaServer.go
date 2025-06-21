package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type ClientConnection struct {
	ID              string
	Conn            *websocket.Conn
	SendCh          chan MajulaPackage
	Cancel          context.CancelFunc
	clientInvokeMap sync.Map
}

type Server struct {
	clients       map[string]*ClientConnection
	node          *Node
	lock          sync.RWMutex
	clientCounter int64
}

func NewServer(node *Node) *Server {
	server := &Server{
		clients:       make(map[string]*ClientConnection),
		node:          node,
		clientCounter: 0,
	}

	node.wsServersMutex.Lock()
	node.wsServers = append(node.wsServers, server)
	node.wsServersMutex.Unlock()

	return server
}

func (s *Server) handleWS(c *gin.Context) {
	clientID := c.Param("target")
	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &ClientConnection{
		ID:     clientID,
		Conn:   conn,
		SendCh: make(chan MajulaPackage, 256),
		Cancel: cancel,
	}

	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()

	s.node.AddClient(clientID)

	go s.readLoop(ctx, client)
	go s.writeLoop(ctx, client)
}

func (s *Server) readLoop(ctx context.Context, client *ClientConnection) {
	defer func() {
		s.lock.Lock()
		delete(s.clients, client.ID)
		s.lock.Unlock()
		s.node.RemoveClient(client.ID)
		client.Cancel() // 通知 writeLoop 退出
		client.Conn.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, message, err := client.Conn.ReadMessage()
			if err != nil {
				log.Println("Read error:", err)
				return
			}
			var pkg MajulaPackage
			if err := json.Unmarshal(message, &pkg); err != nil {
				log.Println("JSON error:", err)
				continue
			}
			go s.handlePackage(client, pkg)
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, client *ClientConnection) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-client.SendCh:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			err := client.Conn.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				log.Println("Write error:", err)
				client.Cancel()
				return
			}
		}
	}
}

func (s *Server) handlePackage(client *ClientConnection, pkg MajulaPackage) {
	switch pkg.Method {
	case "REGISTER_CLIENT":
		s.node.AddClient(client.ID)

	case "SUBSCRIBE":
		topic := pkg.Topic
		s.node.addLocalSub(topic, client.ID, func(topic, from, to string, content []byte) {
			var args map[string]interface{}
			_ = json.Unmarshal(content, &args)
			client.SendCh <- MajulaPackage{
				Method: "PUBLISH",
				Topic:  topic,
				Args:   args,
			}
		})

	case "UNSUBSCRIBE":
		s.node.removeLocalSub(pkg.Topic, client.ID)

	case "PUBLISH":
		argsBytes, _ := json.Marshal(pkg.Args)
		s.node.publishOnTopic(pkg.Topic, string(argsBytes))

	case "SEND":
		targetNodeID, ok1 := pkg.Args["target_node"].(string)
		targetClientID, ok2 := pkg.Args["target_client"].(string)
		content := pkg.Args["content"]

		if !ok1 || !ok2 {
			log.Println("SEND missing target_node or target_client")
			return
		}

		payload := map[string]interface{}{
			"target_client": targetClientID,
			"payload":       content,
		}
		dataBytes, err := json.Marshal(payload)
		if err != nil {
			log.Println("SEND json marshal failed:", err)
			return
		}

		msg := &Message{
			MessageData: MessageData{
				Type: P2PMessage,
				Data: string(dataBytes),
			},
			From:       s.node.ID,
			LastSender: s.node.ID,
			TTL:        100,
		}

		s.node.sendTo(targetNodeID, msg)

	case "REGISTER_RPC":
		s.node.registerRpcService(pkg.Fun, client.ID, RPC_FuncInfo{
			Note: "Client registered RPC",
		}, func(fun string, params map[string]interface{}, from string, to string, invokeId int64) interface{} {
			client.SendCh <- MajulaPackage{
				Method:   "RPC",
				Fun:      fun,
				Args:     params,
				InvokeId: invokeId,
			}
			return map[string]string{"status": "dispatched"}
		})

	case "QUIT":
		s.node.RemoveClient(client.ID)
		s.lock.Lock()
		delete(s.clients, client.ID)
		s.lock.Unlock()

	case "RPC":
		clientInvokeId := pkg.InvokeId
		fun := pkg.Fun
		args := pkg.Args
		fromClientId := client.ID

		targetNodeID, ok1 := args["target_node"].(string)
		providerID, ok2 := args["provider"].(string)

		if !ok1 || !ok2 {
			errInfo := "missing target_node or provider in RPC call"
			log.Println(errInfo)
			client.SendCh <- MajulaPackage{
				Method:   "",
				Fun:      fun,
				InvokeId: clientInvokeId,
				Result:   map[string]interface{}{"error": errInfo},
			}
			return
		}
		delete(args, "target_node")
		delete(args, "provider")

		go func() {
			result, ok := s.node.makeRpcRequest(targetNodeID, providerID, fun, args)

			resp := MajulaPackage{
				Method:   "",
				Fun:      fun,
				InvokeId: clientInvokeId,
				Result:   result,
			}
			if !ok {
				resp.Result = map[string]interface{}{
					"error": fmt.Sprintf("RPC to %s on %s failed", providerID, targetNodeID),
				}
			}

			s.lock.RLock()
			replyClient, exists := s.clients[fromClientId]
			s.lock.RUnlock()
			if exists {
				select {
				case replyClient.SendCh <- resp:
					log.Printf("Send to %s on %s succeed", fromClientId, targetNodeID)
				default:
					log.Printf("client %s SendCh full", fromClientId)
				}
			}
		}()

	default:

	}
}

func (s *Server) SendToClient(clientID string, pkg MajulaPackage) error {
	s.lock.RLock()
	defer s.lock.RUnlock()

	client, ok := s.clients[clientID]
	if !ok {
		return nil
	}

	select {
	case client.SendCh <- pkg:
		return nil
	default:
		return fmt.Errorf("client %s send channel full", clientID)
	}
}

func (s *Server) unregisterClientRpcServices(clientID string) {
	s.node.rpcFuncsMutex.Lock()
	defer s.node.rpcFuncsMutex.Unlock()

	for funcName, providers := range s.node.rpcFuncs {
		if _, ok := providers[clientID]; ok {
			delete(providers, clientID)
			log.Printf("Unregistered RPC: %s by client %s", funcName, clientID)
		}
		if len(providers) == 0 {
			delete(s.node.rpcFuncs, funcName)
		}
	}
}

func (s *Server) Shutdown() {
	s.lock.Lock()
	defer s.lock.Unlock()

	for _, client := range s.clients {
		client.Cancel()
		client.Conn.Close()
		close(client.SendCh)
	}
	s.clients = make(map[string]*ClientConnection)
}

func (s *Server) handleHTTP(c *gin.Context) {
	clientID := c.Param("target")
	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	sendCh := make(chan MajulaPackage, 256)

	client := &ClientConnection{
		ID:     clientID,
		SendCh: sendCh,
		Cancel: cancel,
	}

	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()

	s.node.AddClient(clientID)

	defer func() {
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		cancel()
	}()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.String(http.StatusInternalServerError, "Streaming unsupported")
		return
	}

	log.Printf("HTTP client connected: %s", clientID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("HTTP client %s disconnected", clientID)
			return
		case msg, ok := <-client.SendCh:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
		case <-c.Request.Context().Done():
			return
		case <-time.After(15 * time.Second):
			fmt.Fprintf(c.Writer, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleHTTPPost(c *gin.Context) {
	clientID := c.Param("target")
	if clientID == "" || clientID == "/" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client ID is required"})
		return
	}

	var pkg MajulaPackage
	if err := c.ShouldBindJSON(&pkg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	s.lock.RLock()
	client, ok := s.clients[clientID]
	s.lock.RUnlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}

	go s.handlePackage(client, pkg)
	c.JSON(http.StatusOK, gin.H{"status": "dispatched"})
}

func setupRoutes(server *Server) *gin.Engine {
	r := gin.Default()

	r.GET("/ws/", server.handleWS)
	r.GET("/ws/:target", server.handleWS)

	r.GET("/http", server.handleHTTP)
	r.GET("/http/:target", server.handleHTTP)

	r.POST("/http/:target", server.handleHTTPPost)

	r.GET("/sub", server.handleSubscribeGET)
	r.GET("/sub/:target", server.handleSubscribeGET)

	r.GET("/pub", server.handlePublishGET)
	r.GET("/pub/:target", server.handlePublishGET)

	r.GET("/rpc", server.handleRpcGET)
	r.GET("/rpc/:target", server.handleRpcGET)

	r.GET("/send", server.handleSendGET)
	r.GET("/send/:target", server.handleSendGET)

	r.GET("/listrpc", server.handleListRpcGET)
	r.GET("/listrpc/:target", server.handleListRpcGET)
	return r
}

func (s *Server) generateClientID() string {
	id := atomic.AddInt64(&s.clientCounter, 1)
	return fmt.Sprintf("client-%d", id)
}

func (s *Server) handleSubscribeGET(c *gin.Context) {
	clientID := c.Param("target")
	topic := c.Query("topic")

	if topic == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing topic"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	sendCh := make(chan MajulaPackage, 256)

	client := &ClientConnection{
		ID:     clientID,
		SendCh: sendCh,
		Cancel: cancel,
	}

	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()

	s.node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		log.Printf("Temporary client %s unsubscribed and removed", clientID)
	}()

	s.node.addLocalSub(topic, clientID, func(topic, from, to string, content []byte) {
		var args map[string]interface{}
		_ = json.Unmarshal(content, &args)
		select {
		case client.SendCh <- MajulaPackage{
			Method: "PUBLISH",
			Topic:  topic,
			Args:   args,
		}:
		default:
			log.Printf("Client %s send buffer full, message dropped", clientID)
		}
	})

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.String(http.StatusInternalServerError, "Streaming unsupported")
		return
	}

	log.Printf("Temporary client %s subscribed to topic %s", clientID, topic)

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Request.Context().Done():
			cancel()
			return
		case msg, ok := <-client.SendCh:
			if !ok {
				return
			}
			data, _ := json.Marshal(msg)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			flusher.Flush()
		case <-time.After(15 * time.Second):
			fmt.Fprintf(c.Writer, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handlePublishGET(c *gin.Context) {
	clientID := c.Param("target")
	topic := c.Query("topic")
	message := c.Query("msg")

	if topic == "" || message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing topic or msg"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	client := &ClientConnection{
		ID:     clientID,
		SendCh: make(chan MajulaPackage, 16),
		Cancel: cancel,
	}

	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()
	s.node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		log.Printf("Client %s removed after publish", clientID)
	}()

	s.node.publishOnTopic(topic, message)

	c.JSON(http.StatusOK, gin.H{
		"status":  "published",
		"topic":   topic,
		"client":  clientID,
		"message": message,
	})
}

func (s *Server) handleRpcGET(c *gin.Context) {
	clientID := c.Param("target")
	fun := c.Query("fun")
	toNode := c.Query("to_node")
	provider := c.Query("provider")
	argsRaw := c.Query("args")

	if fun == "" || toNode == "" || provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required fields: fun, to_node, provider"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	var args map[string]interface{}
	if argsRaw != "" {
		if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid args JSON"})
			return
		}
	} else {
		args = make(map[string]interface{})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client := &ClientConnection{
		ID:     clientID,
		SendCh: make(chan MajulaPackage, 16),
		Cancel: cancel,
	}
	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()
	s.node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after RPC", clientID)
	}()

	go func() {
		result, ok := s.node.makeRpcRequest(toNode, provider, fun, args)
		resp := gin.H{
			"fun":      fun,
			"args":     args,
			"client":   clientID,
			"provider": provider,
			"node":     toNode,
			"result":   result,
			"success":  ok,
		}
		if !ok {
			resp["error"] = fmt.Sprintf("RPC to %s on %s failed", provider, toNode)
		}
		c.JSON(http.StatusOK, resp)
	}()

	<-ctx.Done()
}

func (s *Server) handleSendGET(c *gin.Context) {
	clientID := c.Param("target")
	toNode := c.Query("to_node")
	toClient := c.Query("to_client")
	msg := c.Query("msg")

	if toNode == "" || toClient == "" || msg == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "missing required fields: to_node, to_client, msg",
		})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client := &ClientConnection{
		ID:     clientID,
		SendCh: make(chan MajulaPackage, 16),
		Cancel: cancel,
	}

	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()
	s.node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after SEND", clientID)
	}()

	payload := map[string]interface{}{
		"target_client": toClient,
		"payload":       msg,
	}
	dataBytes, _ := json.Marshal(payload)

	message := &Message{
		MessageData: MessageData{
			Type: P2PMessage,
			Data: string(dataBytes),
		},
		From:       s.node.ID,
		LastSender: s.node.ID,
		TTL:        100,
	}

	s.node.sendTo(toNode, message)

	c.JSON(http.StatusOK, gin.H{
		"status":      "sent",
		"from_client": clientID,
		"to_node":     toNode,
		"to_client":   toClient,
		"message":     msg,
	})
}

func (s *Server) handleListRpcGET(c *gin.Context) {
	clientID := c.Param("target")
	targetNode := c.Query("to_node")
	provider := c.Query("provider")

	if targetNode == "" || provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing to_node or provider"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client := &ClientConnection{
		ID:     clientID,
		SendCh: make(chan MajulaPackage, 16),
		Cancel: cancel,
	}
	s.lock.Lock()
	s.clients[clientID] = client
	s.lock.Unlock()
	s.node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.lock.Lock()
		delete(s.clients, clientID)
		s.lock.Unlock()
		s.node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after listrpc", clientID)
	}()

	params := map[string]interface{}{
		"rpcProvider": provider,
	}

	result, ok := s.node.makeRpcRequest(targetNode, "init", "allrpcs", params)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "RPC call failed or timed out"})
		return
	}

	rpcs := []map[string]string{}
	if list, ok := result.([]interface{}); ok {
		for _, item := range list {
			if m, ok := item.(map[string]interface{}); ok {
				rpcName, _ := m["name"].(string)
				note, _ := m["note"].(string)
				rpcs = append(rpcs, map[string]string{
					"name": rpcName,
					"note": note,
				})
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"node":     targetNode,
		"provider": provider,
		"rpcs":     rpcs,
	})
}
