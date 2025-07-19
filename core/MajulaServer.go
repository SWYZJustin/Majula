package core

import (
	"Majula/api"
	"Majula/common"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
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
	SendCh          chan api.MajulaPackage
	Cancel          context.CancelFunc
	ClientInvokeMap sync.Map
}

type Server struct {
	Clients       map[string]*ClientConnection
	Node          *Node
	Lock          sync.RWMutex
	ClientCounter int64
	Port          string
}

func NewServer(node *Node, wport string) *Server {
	server := &Server{
		Clients:       make(map[string]*ClientConnection),
		Node:          node,
		ClientCounter: 0,
		Port:          wport,
	}

	node.WsServersMutex.Lock()
	node.WsServers = append(node.WsServers, server)
	node.WsServersMutex.Unlock()

	return server
}

func SetupRoutes(server *Server) *gin.Engine {
	r := gin.Default()
	r.Use(Cors())
	r.Use(server.ReverseProxy())
	rg := r.Group("/majula")
	registerDualMethod(rg, "/ws", server.handleWS, true)
	registerDualMethod(rg, "/h", server.handleHTTP, true)
	registerDualMethod(rg, "/sub", server.handleSubscribe, true)
	registerDualMethod(rg, "/pub", server.handlePublish, true)
	registerDualMethod(rg, "/rpc", server.handleRpc, true)
	registerDualMethod(rg, "/send", server.handleSend, true)
	registerDualMethod(rg, "/list_rpc", server.handleListRpc, true)
	registerDualMethod(rg, "/map", server.handleNginxFrp, true)
	registerDualMethod(rg, "/frp", server.handleFrp, false)
	registerDualMethod(rg, "/upload", server.handleFileUpload, false)
	registerDualMethod(rg, "/download", server.handleFileDownload, false)
	return r
}

func (s *Server) handleWS(c *gin.Context) {
	target, _ := parseGinParameters(c)
	clientID := target
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
		SendCh: make(chan api.MajulaPackage, 256),
		Cancel: cancel,
	}

	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()

	s.Node.AddClient(clientID)

	go s.readLoop(ctx, client)
	go s.writeLoop(ctx, client)
}

func (s *Server) readLoop(ctx context.Context, client *ClientConnection) {
	defer func() {
		s.Lock.Lock()
		delete(s.Clients, client.ID)
		s.Lock.Unlock()
		s.Node.RemoveClient(client.ID)
		client.Cancel()
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
			var pkg api.MajulaPackage
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

func (s *Server) handleClientRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.AddClient(client.ID)
}

func (s *Server) handleSubscribePackage(client *ClientConnection, pkg api.MajulaPackage) {
	topic := pkg.Topic
	if topic == "" {
		return
	}
	s.Node.addLocalSub(topic, client.ID, func(topic, from, to string, content []byte) {
		var args map[string]interface{}
		_ = json.Unmarshal(content, &args)
		client.SendCh <- api.MajulaPackage{
			Method: "SUB_RESULT",
			Topic:  topic,
			Args:   args,
		}
	})
}

func (s *Server) handleUnsubscribePackage(client *ClientConnection, pkg api.MajulaPackage) {
	topic := pkg.Topic
	if topic == "" {
		return
	}
	s.Node.removeLocalSub(topic, client.ID)
}

func (s *Server) handlePublishPackage(client *ClientConnection, pkg api.MajulaPackage) {
	argsBytes, _ := json.Marshal(pkg.Args)
	s.Node.publishOnTopic(pkg.Topic, string(argsBytes))
}

func (s *Server) handleSendPackage(client *ClientConnection, pkg api.MajulaPackage) {
	targetNode, ok1 := pkg.Args["target_node"].(string)
	targetClient, ok2 := pkg.Args["target_client"].(string)
	content := pkg.Args["content"]

	if !ok1 || !ok2 {
		log.Println("SEND missing target_node or target_client")
		return
	}

	payload := map[string]interface{}{
		"target_client": targetClient,
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
		From:       s.Node.ID,
		LastSender: s.Node.ID,
		TTL:        common.DefaultMessageTTL,
	}

	s.Node.sendTo(targetNode, msg)
}

func (s *Server) handleRPCRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.registerRpcService(pkg.Fun, client.ID, RPC_FuncInfo{
		Note: "Client registered RPC",
	}, func(fun string, params map[string]interface{}, from string, to string, invokeId int64) interface{} {
		client.SendCh <- api.MajulaPackage{
			Method:   "RPC_CALL_FROM_REMOTE",
			Fun:      fun,
			Args:     params,
			InvokeId: invokeId,
		}
		return map[string]string{"status": "dispatched"}
	})
}

func (s *Server) handleRPCUnregisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	targetFun := pkg.Fun
	s.Node.removeLocalSub(targetFun, client.ID)
}

func (s *Server) handleQuitPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.RemoveClient(client.ID)
	s.Lock.Lock()
	delete(s.Clients, client.ID)
	s.Lock.Unlock()
}

func (s *Server) handleRPCCallPackage(client *ClientConnection, pkg api.MajulaPackage) {
	clientInvokeId := pkg.InvokeId
	fun := pkg.Fun
	args := pkg.Args
	fromClientId := client.ID

	targetNode, ok1 := args["_target_node"].(string)
	provider, ok2 := args["_provider"].(string)

	if !ok1 || !ok2 {
		errInfo := "missing target_node or provider in RPC call"
		log.Println(errInfo)
		client.SendCh <- api.MajulaPackage{
			Method:   "",
			Fun:      fun,
			InvokeId: clientInvokeId,
			Result:   map[string]interface{}{"error": errInfo},
		}
		return
	}
	delete(args, "_target_node")
	delete(args, "_provider")

	go func() {
		result, ok := s.Node.MakeRpcRequest(targetNode, provider, fun, args)

		resp := api.MajulaPackage{
			Method:   "RPC_RESULT",
			Fun:      fun,
			InvokeId: clientInvokeId,
			Result:   result,
		}
		if !ok {
			resp.Result = map[string]interface{}{
				"error": fmt.Sprintf("RPC to %s on %s failed", provider, targetNode),
			}
		}

		s.Lock.RLock()
		replyClient, exists := s.Clients[fromClientId]
		s.Lock.RUnlock()
		if exists {
			select {
			case replyClient.SendCh <- resp:
				log.Printf("Send to %s on %s succeed", fromClientId, targetNode)
			default:
				log.Printf("client %s SendCh full", fromClientId)
			}
		}
	}()
}

func (s *Server) handleFRPRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	localAddr, ok2 := pkg.Args["local_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)
	remoteAddr, ok4 := pkg.Args["remote_addr"].(string)

	if !ok1 || !ok2 || !ok3 || !ok4 {
		log.Println("frp register missing args")
		return
	}

	err := s.Node.StubManager.RegisterFRPWithCode(code, localAddr, remoteNode, remoteAddr)
	if err != nil {
		log.Printf("Failed to register FRP error: %v", err)
	}
}

func (s *Server) handleFRPRegisterWithAddrPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	remoteAddr, ok2 := pkg.Args["remote_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)
	if !ok1 || !ok2 || !ok3 {
		log.Println("frp register missing args")
	}

	_, err := s.Node.StubManager.RegisterFRPWithoutCode(localAddr, remoteNode, remoteAddr)
	if err != nil {
		log.Printf("Failed to register FRP error: %v", err)
	}
}

func (s *Server) handleFRPRegisterTwoSidePackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	targetAddr, ok3 := pkg.Args["target_addr"].(string)
	isServer, ok4 := pkg.Args["is_server"].(bool)

	if !ok1 || !ok2 || !ok3 || !ok4 {
		log.Println("frp register two side missing args")
		return
	}
	err := s.Node.StubManager.RegisterFRPSimplified(code, remoteNode, targetAddr, isServer)
	if err != nil {
		log.Printf("Failed to register FRP error: %v", err)
	}
}

func (s *Server) handleStartFRPWithRegistrationPackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	if !ok1 {
		log.Println("frp start missing args")
		return
	}
	err := s.Node.StubManager.RunFRPDynamicWithRegistration(code)
	if err != nil {
		log.Printf("Failed to start FRP with code: %v", err)
	}
}

func (s *Server) handleStartFRPWithoutRegistrationPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	remoteAddr, ok2 := pkg.Args["remote_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)

	if !ok1 || !ok2 || !ok3 {
		log.Println("frp start missing args")
		return
	}

	_, err := s.Node.StubManager.RegisterFRPWithoutCode(localAddr, remoteNode, remoteAddr)
	if err != nil {
		log.Printf("Failed to register FRP")
		return
	}
	err = s.Node.StubManager.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
	if err != nil {
		log.Printf("Failed to start FRP")
		return
	}
}

func (s *Server) handleStartFRPWithLocalAddressPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	if !ok1 {
		log.Println("frp start missing args")
		return
	}

	err := s.Node.StubManager.RunFRPDynamicWithRegistrationLocalAddr(localAddr)
	if err != nil {
		log.Printf("Failed to start FRP")
		return
	}
}

func (s *Server) handleRegisterNginxFRPAndRunPackage(client *ClientConnection, pkg api.MajulaPackage) {
	var extraArgs map[string]string
	extraRaw, ok := pkg.Args["extra_args"].(string)
	if !ok {
		log.Println("nginx frp args wrong")
		return
	}
	err := json.Unmarshal([]byte(extraRaw), &extraArgs)
	if err != nil {
		log.Printf("Failed to unmarshal extra_args: %v", err)
		return
	}

	mappedAddr, ok1 := pkg.Args["mapped_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	hostAddr, ok3 := pkg.Args["remote_url"].(string)
	if !ok1 || !ok2 || !ok3 {
		log.Println("nginx frp args wrong")
		return
	}
	err = s.RegisterNginxFrp(mappedAddr, remoteNode, hostAddr, extraArgs)
	if err != nil {
		log.Printf("Failed to register and run Nginx frp error: %v", err)
		return
	}
}

func (s *Server) handleUnregisterNginxFRPPackage(client *ClientConnection, pkg api.MajulaPackage) {
	var extraArgs map[string]string
	extraRaw, ok := pkg.Args["extra_args"].(string)
	if !ok {
		log.Println("nginx frp args wrong")
		return
	}
	err := json.Unmarshal([]byte(extraRaw), &extraArgs)
	if err != nil {
		log.Printf("Failed to unmarshal extra_args: %v", err)
		return
	}

	mappedAddr, ok1 := pkg.Args["mapped_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	hostAddr, ok3 := pkg.Args["remote_url"].(string)
	if !ok1 || !ok2 || !ok3 {
		log.Println("nginx frp args wrong")
		return
	}
	err = s.RemoveNginxFrp(mappedAddr, remoteNode, hostAddr, extraArgs)
	if err != nil {
		log.Printf("Failed to register and run Nginx frp error: %v", err)
		return
	}
}

func (s *Server) handleTransferFileToRemotePackage(client *ClientConnection, pkg api.MajulaPackage) {
	localPath, ok1 := pkg.Args["local_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	remotePath, ok3 := pkg.Args["remote_path"].(string)
	if !ok1 || !ok2 || !ok3 {
		log.Println("frp transfer missing args")
		return
	}
	err := s.Node.StubManager.TransferFileToRemoteWithoutRegistration(remoteNode, localPath, remotePath)
	if err != nil {
		log.Printf("Failed to transfer file to remote: %v", err)
		return
	}
}

func (s *Server) handleDownloadFileFromRemotePackage(client *ClientConnection, pkg api.MajulaPackage) {
	localPath, ok1 := pkg.Args["local_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	remotePath, ok3 := pkg.Args["remote_path"].(string)
	if !ok1 || !ok2 || !ok3 {
		log.Println("frp download file missing args")
	}
	err := s.Node.StubManager.DownloadFileFromRemoteWithoutRegistration(remoteNode, remotePath, localPath)
	if err != nil {
		log.Printf("Failed to download file from remote: %v", err)
		return
	}
}

func (s *Server) handlePackage(client *ClientConnection, pkg api.MajulaPackage) {
	switch pkg.Method {
	case "REGISTER_CLIENT":
		go s.handleClientRegisterPackage(client, pkg)

	case "SUBSCRIBE":
		go s.handleSubscribePackage(client, pkg)

	case "UNSUBSCRIBE":
		go s.handleUnsubscribePackage(client, pkg)

	case "PUBLISH":
		go s.handlePublishPackage(client, pkg)

	case "SEND":
		go s.handleSendPackage(client, pkg)

	case "REGISTER_RPC":
		go s.handleRPCRegisterPackage(client, pkg)

	case "UNREGISTER_RPC":
		go s.handleRPCUnregisterPackage(client, pkg)

	case "QUIT":
		go s.handleQuitPackage(client, pkg)

	case "RPC":
		go s.handleRPCCallPackage(client, pkg)

	case "REGISTER_FRP":
		go s.handleFRPRegisterPackage(client, pkg)

	case "REGISTER_FRP_WITH_ADDR":
		go s.handleFRPRegisterPackage(client, pkg)

	case "START_FRP_LISTENER_WITH_REGISTRATION":
		go s.handleStartFRPWithRegistrationPackage(client, pkg)

	case "START_FRP_LISTENER_WITHOUT_REGISTRATION":
		go s.handleStartFRPWithoutRegistrationPackage(client, pkg)

	case "START_FRP_LISTENER_WITH_LOCAL_ADDR":
		go s.handleStartFRPWithLocalAddressPackage(client, pkg)

	case "REGISTER_NGINX_FRP_AND_RUN":
		go s.handleRegisterNginxFRPAndRunPackage(client, pkg)

	case "UNREGISTER_NGINX_FRP":
		go s.handleUnregisterNginxFRPPackage(client, pkg)

	case "UPLOAD_FILE":
		go s.handleTransferFileToRemotePackage(client, pkg)

	case "DOWNLOAD_FILE":
		go s.handleDownloadFileFromRemotePackage(client, pkg)

	default:

	}
}

func (s *Server) SendToClient(clientID string, pkg api.MajulaPackage) error {
	s.Lock.RLock()
	defer s.Lock.RUnlock()

	client, ok := s.Clients[clientID]
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

func (s *Server) UnregisterClientRpcServices(clientID string) {
	s.Node.RpcFuncsMutex.Lock()
	defer s.Node.RpcFuncsMutex.Unlock()

	for funcName, providers := range s.Node.RpcFuncs {
		if _, ok := providers[clientID]; ok {
			delete(providers, clientID)
			log.Printf("Unregistered RPC: %s by client %s", funcName, clientID)
		}
		if len(providers) == 0 {
			delete(s.Node.RpcFuncs, funcName)
		}
	}
}

func (s *Server) Shutdown() {
	s.Lock.Lock()
	defer s.Lock.Unlock()

	for _, client := range s.Clients {
		client.Cancel()
		client.Conn.Close()
		close(client.SendCh)
	}
	s.Clients = make(map[string]*ClientConnection)
}

func (s *Server) handleHTTP(c *gin.Context) {
	target, _ := parseGinParameters(c)
	clientID := target
	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	sendCh := make(chan api.MajulaPackage, 256)

	client := &ClientConnection{
		ID:     clientID,
		SendCh: sendCh,
		Cancel: cancel,
	}

	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()

	s.Node.AddClient(clientID)

	defer func() {
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
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

func registerDualMethod(rg *gin.RouterGroup, path string, handler gin.HandlerFunc, withTarget bool) {
	rg.GET(path, handler)
	rg.POST(path, handler)

	if withTarget {
		targetPath := path
		if !strings.HasSuffix(path, "/") {
			targetPath += "/"
		}
		targetPath += ":target"

		rg.GET(targetPath, handler)
		rg.POST(targetPath, handler)
	}
}

func (s *Server) generateClientID() string {
	id := atomic.AddInt64(&s.ClientCounter, 1)
	return fmt.Sprintf("client-%d", id)
}

func (s *Server) handleSubscribe(c *gin.Context) {
	target, params := parseGinParameters(c)
	clientID := target
	topic, _ := params["topic"].(string)

	if topic == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing topic"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	ctx, cancel := context.WithCancel(context.Background())
	sendCh := make(chan api.MajulaPackage, 256)

	client := &ClientConnection{
		ID:     clientID,
		SendCh: sendCh,
		Cancel: cancel,
	}

	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()

	s.Node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
		log.Printf("Temporary client %s unsubscribed and removed", clientID)
	}()

	s.Node.addLocalSub(topic, clientID, func(topic, from, to string, content []byte) {
		var args map[string]interface{}
		_ = json.Unmarshal(content, &args)
		select {
		case client.SendCh <- api.MajulaPackage{
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

func (s *Server) handlePublish(c *gin.Context) {
	target, params := parseGinParameters(c)
	clientID := target
	topic, _ := params["topic"].(string)
	message, _ := params["msg"].(string)

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
		SendCh: make(chan api.MajulaPackage, 16),
		Cancel: cancel,
	}

	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()
	s.Node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
		log.Printf("Client %s removed after publish", clientID)
	}()

	s.Node.publishOnTopic(topic, message)

	c.JSON(http.StatusOK, gin.H{
		"status":  "published",
		"topic":   topic,
		"client":  clientID,
		"message": message,
	})
}

func (s *Server) handleRpc(c *gin.Context) {
	clientID, params := parseGinParameters(c)

	fun, _ := params["fun"].(string)
	targetNode, _ := params["target_node"].(string)
	provider, _ := params["provider"].(string)

	if fun == "" || targetNode == "" || provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required fields: fun, targetNode, provider"})
		return
	}

	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	argsRaw, hasRaw := params["args"].(string)
	var args map[string]interface{}
	if hasRaw {
		if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid args JSON"})
			return
		}
	} else {
		args = make(map[string]interface{})
		for k, v := range params {
			if k != "fun" && k != "target_node" && k != "provider" {
				args[k] = v
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client := &ClientConnection{
		ID:     clientID,
		SendCh: make(chan api.MajulaPackage, 16),
		Cancel: cancel,
	}
	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()
	s.Node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after RPC", clientID)
	}()

	go func() {
		result, ok := s.Node.MakeRpcRequest(targetNode, provider, fun, args)
		resp := gin.H{
			"fun":         fun,
			"args":        args,
			"client":      clientID,
			"provider":    provider,
			"target_node": targetNode,
			"result":      result,
			"success":     ok,
		}
		if !ok {
			resp["error"] = fmt.Sprintf("RPC to %s on %s failed", provider, targetNode)
		}
		c.JSON(http.StatusOK, resp)
	}()

	<-ctx.Done()
}

func (s *Server) handleSend(c *gin.Context) {
	clientID, params := parseGinParameters(c)

	targetNode, _ := params["target_node"].(string)
	targetClient, _ := params["target_client"].(string)
	msg, _ := params["msg"].(string)

	if targetNode == "" || targetClient == "" || msg == "" {
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
		SendCh: make(chan api.MajulaPackage, 16),
		Cancel: cancel,
	}

	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()
	s.Node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after SEND", clientID)
	}()

	payload := map[string]interface{}{
		"target_client": targetClient,
		"payload":       msg,
	}
	dataBytes, _ := json.Marshal(payload)

	message := &Message{
		MessageData: MessageData{
			Type: P2PMessage,
			Data: string(dataBytes),
		},
		From:       s.Node.ID,
		LastSender: s.Node.ID,
		TTL:        common.DefaultMessageTTL,
	}

	s.Node.sendTo(targetNode, message)

	c.JSON(http.StatusOK, gin.H{
		"status":        "sent",
		"source_client": clientID,
		"target_node":   targetNode,
		"target_client": targetClient,
		"message":       msg,
	})
}

func (s *Server) handleListRpc(c *gin.Context) {
	clientID, params := parseGinParameters(c)

	targetNode, _ := params["target_node"].(string)
	provider, _ := params["provider"].(string)

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
		SendCh: make(chan api.MajulaPackage, 16),
		Cancel: cancel,
	}
	s.Lock.Lock()
	s.Clients[clientID] = client
	s.Lock.Unlock()
	s.Node.AddClient(clientID)

	go func() {
		<-ctx.Done()
		s.Lock.Lock()
		delete(s.Clients, clientID)
		s.Lock.Unlock()
		s.Node.RemoveClient(clientID)
		log.Printf("Temporary client %s removed after listrpc", clientID)
	}()

	paramsRpc := map[string]interface{}{
		"rpc_provider": provider,
	}

	result, ok := s.Node.MakeRpcRequest(targetNode, "init", "allrpcs", paramsRpc)
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
		"target_node": targetNode,
		"provider":    provider,
		"rpcs":        rpcs,
	})
}

func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		origin := c.Request.Header.Get("Origin")

		if origin != "" {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE,UPDATE")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Length, X-CSRF-Token, Token,session,X_Requested_With,Accept, Origin, HostAddr, Connection, Accept-Encoding, Accept-Language,DNT, X-CustomHeader, Keep-Alive, User-Agent, X-Requested-With, If-Modified-Since, Cache-Control, Content-Type, Pragma")
			c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers,Cache-Control,Content-Language,Content-Type,Expires,Last-Modified,Pragma,FooBar")
			c.Header("Access-Control-Max-Age", "172800")
			c.Header("Access-Control-Allow-Credentials", "false")
			c.Set("content-type", "application/json")
		}

		if method == "OPTIONS" {
			c.JSON(http.StatusOK, "Options Request!")
		}

		c.Next()
	}
}

func parseGinParameters(c *gin.Context) (string, map[string]interface{}) {
	target := c.Param("target")
	if target != "" && target[0] == '/' {
		target = target[1:]
	}
	params := make(map[string]interface{})
	if c.Request.Body != nil {
		body, err := ioutil.ReadAll(c.Request.Body)
		if err == nil && len(body) > 0 {
			var jsonBody map[string]interface{}
			if err := json.Unmarshal(body, &jsonBody); err == nil {
				for k, v := range jsonBody {
					params[k] = v
				}
			}
		}
	}
	_ = c.Request.ParseForm()
	for k, v := range c.Request.Form {
		params[k] = strings.Join(v, " ")
	}
	return target, params
}

func (s *Server) handleFrp(c *gin.Context) {
	target, params := parseGinParameters(c)
	_ = target

	localAddr, _ := params["local_addr"].(string)
	remoteNode, _ := params["remote_node"].(string)
	remoteAddr, _ := params["remote_addr"].(string)

	if localAddr == "" || remoteNode == "" || remoteAddr == "" {
		c.JSON(400, gin.H{
			"error": "Missing required parameters: localAddr, remote_node, remote_addr",
		})
		return
	}

	err := s.Node.StubManager.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
	if err != nil {
		c.JSON(500, gin.H{
			"error": fmt.Sprintf("Failed to start dynamic FRP listener: %v", err),
		})
		return
	}

	c.JSON(200, gin.H{
		"status":      "ok",
		"message":     "Dynamic FRP listener started",
		"local_addr":  localAddr,
		"remote_node": remoteNode,
		"remote_addr": remoteAddr,
	})
}

func (s *Server) handleFileUpload(c *gin.Context) {
	_, params := parseGinParameters(c)

	remoteNode, _ := params["remote_node"].(string)
	localPath, _ := params["local_path"].(string)
	remotePath, _ := params["remote_path"].(string)

	if remoteNode == "" || localPath == "" || remotePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing remote_node, local_path, or remote_path"})
		return
	}

	go func() {
		err := s.Node.StubManager.TransferFileToRemoteWithoutRegistration(remoteNode, localPath, remotePath)
		if err != nil {
			log.Printf("UPLOAD_FILE_DYNAMIC error: %v", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":      "upload dispatched",
		"remote_node": remoteNode,
		"local_path":  localPath,
		"remote_path": remotePath,
	})
}

func (s *Server) handleFileDownload(c *gin.Context) {
	_, params := parseGinParameters(c)

	remoteNode, _ := params["remote_node"].(string)
	remotePath, _ := params["remote_path"].(string)
	localPath, _ := params["local_path"].(string)

	if remoteNode == "" || remotePath == "" || localPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing remote_node, remote_path, or local_path"})
		return
	}

	go func() {
		err := s.Node.StubManager.DownloadFileFromRemoteWithoutRegistration(remoteNode, remotePath, localPath)
		if err != nil {
			log.Printf("DOWNLOAD_FILE_DYNAMIC error: %v", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":      "download dispatched",
		"remote_node": remoteNode,
		"remote_path": remotePath,
		"local_path":  localPath,
	})
}
