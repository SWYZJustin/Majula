package core

import (
	"Majula/api"
	"Majula/common"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

type pendingRpcEntry struct {
	originInvokeId int64
	fromClientId   string
	ch             chan interface{}
}

// 生成下一个本地RPC调用ID。
// 返回：自增的调用ID。
func (s *Server) nextLocalInvokeId() int64 {
	return atomic.AddInt64(&s.rpcInvokeCounter, 1)
}

// Server Majula服务端，管理所有客户端连接和RPC。
type Server struct {
	Clients          map[string]*ClientConnection
	Node             *Node
	Lock             sync.RWMutex
	ClientCounter    int64
	Port             string
	pendingRpc       sync.Map
	rpcInvokeCounter int64
}

// 创建一个新的Server实例。
// 参数：node - 所属节点，wport - 监听端口。
// 返回：*Server 新建的服务端对象。
func NewServer(node *Node, wport string) *Server {
	server := &Server{
		Clients:          make(map[string]*ClientConnection),
		Node:             node,
		ClientCounter:    0,
		Port:             wport,
		pendingRpc:       sync.Map{},
		rpcInvokeCounter: 0,
	}

	node.WsServersMutex.Lock()
	node.WsServers = append(node.WsServers, server)
	node.WsServersMutex.Unlock()

	return server
}

// 配置Gin路由，注册所有Majula相关接口。
// 参数：server - Server实例。
// 返回：*gin.Engine Gin引擎。
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
	registerDualMethod(rg, "/join_raft_learner", server.handleJoinRaftLearner, false)
	registerDualMethod(rg, "/join_raft_learner_with_fast_sync", server.handleJoinRaftLearnerWithFastSync, false)
	registerDualMethod(rg, "/leave_raft_learner", server.handleLeaveRaftLearner, false)
	registerDualMethod(rg, "/elect/join", server.handleElectJoin, false)
	registerDualMethod(rg, "/elect/giveup", server.handleElectGiveUp, false)
	registerDualMethod(rg, "/elect/leave", server.handleElectLeave, false)
	registerDualMethod(rg, "/elect/status", server.handleElectStatus, false)
	registerDualMethod(rg, "/raft/put", server.handleRaftPut, false)
	registerDualMethod(rg, "/raft/delete", server.handleRaftDelete, false)
	registerDualMethod(rg, "/raft/get", server.handleRaftGet, false)
	registerDualMethod(rg, "/connect_to_node", server.handleConnectToNode, false)
	return r
}

// 处理WebSocket连接请求，建立客户端连接。
// 参数：c - Gin上下文。
func (s *Server) handleWS(c *gin.Context) {
	target, _ := parseGinParameters(c)
	clientID := target
	if clientID == "" || clientID == "/" {
		clientID = s.generateClientID()
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		Error("WebSocket升级错误", "错误=", err)
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

// 客户端消息读取主循环。
// 参数：ctx - 上下文，client - 客户端连接。
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
				Error("读取错误", "错误=", err)
				return
			}
			var pkg api.MajulaPackage
			if err := json.Unmarshal(message, &pkg); err != nil {
				Error("JSON解析错误", "错误=", err)
				continue
			}
			go s.handlePackage(client, pkg)
		}
	}
}

// 客户端消息写入主循环。
// 参数：ctx - 上下文，client - 客户端连接。
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
				Error("写入错误", "错误=", err)
				client.Cancel()
				return
			}
		}
	}
}

// 处理客户端注册包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleClientRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.AddClient(client.ID)
	Log("客户端已注册", "客户端ID=", client.ID)
}

// 处理客户端订阅包。
// 参数：client - 客户端连接，pkg - 订阅包。
func (s *Server) handleSubscribePackage(client *ClientConnection, pkg api.MajulaPackage) {
	topic := pkg.Topic
	if topic == "" {
		return
	}
	s.Node.addLocalSub(topic, client.ID, func(topic, from, to string, content []byte) {
		var args map[string]interface{}
		_ = common.UnmarshalAny(content, &args)
		client.SendCh <- api.MajulaPackage{
			Method: "SUB_RESULT",
			Topic:  topic,
			Args:   args,
		}
	})
}

// 处理客户端取消订阅包。
// 参数：client - 客户端连接，pkg - 取消订阅包。
func (s *Server) handleUnsubscribePackage(client *ClientConnection, pkg api.MajulaPackage) {
	topic := pkg.Topic
	if topic == "" {
		return
	}
	s.Node.removeLocalSub(topic, client.ID)
}

// 处理客户端发布消息包。
// 参数：client - 客户端连接，pkg - 发布包。
func (s *Server) handlePublishPackage(client *ClientConnection, pkg api.MajulaPackage) {
	argsBytes, _ := common.MarshalAny(pkg.Args)
	s.Node.publishOnTopic(pkg.Topic, string(argsBytes))
}

// 处理客户端发送消息包。
// 参数：client - 客户端连接，pkg - 发送包。
func (s *Server) handleSendPackage(client *ClientConnection, pkg api.MajulaPackage) {
	targetNode, ok1 := pkg.Args["target_node"].(string)
	targetClient, ok2 := pkg.Args["target_client"].(string)
	content := pkg.Args["content"]

	if !ok1 || !ok2 {
		Error("SEND缺少目标节点或目标客户端")
		return
	}

	payload := map[string]interface{}{
		"target_client": targetClient,
		"payload":       content,
	}
	dataBytes, err := common.MarshalAny(payload)
	if err != nil {
		Error("SEND JSON序列化失败", "错误=", err)
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

// 处理客户端RPC注册包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleRPCRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.registerRpcService(pkg.Fun, client.ID, RPC_FuncInfo{
		Note: "Client registered RPC",
	}, func(fun string, params map[string]interface{}, from string, to string, originInvokeId int64) interface{} {
		localInvokeId := s.nextLocalInvokeId()
		ch := make(chan interface{}, 1)

		s.pendingRpc.Store(localInvokeId, pendingRpcEntry{
			originInvokeId: originInvokeId,
			fromClientId:   from,
			ch:             ch,
		})

		client.SendCh <- api.MajulaPackage{
			Method:   "RPC_CALL_FROM_REMOTE",
			Fun:      fun,
			Args:     params,
			InvokeId: localInvokeId,
		}

		select {
		case result := <-ch:
			s.pendingRpc.Delete(localInvokeId)
			return result
		case <-time.After(10 * time.Second):
			s.pendingRpc.Delete(localInvokeId)
			return map[string]interface{}{"error": "timeout waiting for result"}
		}
	})
}

// 处理客户端返回结果包。
// 参数：client - 客户端连接，pkg - 结果包。
func (s *Server) handleReturnResultPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localInvokeId := pkg.InvokeId

	if entryRaw, ok := s.pendingRpc.Load(localInvokeId); ok {
		s.pendingRpc.Delete(localInvokeId)
		if entry, ok := entryRaw.(pendingRpcEntry); ok {
			entry.ch <- pkg.Result
		}
	}
}

// 处理客户端RPC注销包。
// 参数：client - 客户端连接，pkg - 注销包。
func (s *Server) handleRPCUnregisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	targetFun := pkg.Fun
	s.Node.removeLocalSub(targetFun, client.ID)
}

// 处理客户端退出包。
// 参数：client - 客户端连接，pkg - 退出包。
func (s *Server) handleQuitPackage(client *ClientConnection, pkg api.MajulaPackage) {
	s.Node.RemoveClient(client.ID)
	s.Lock.Lock()
	delete(s.Clients, client.ID)
	s.Lock.Unlock()
}

// 处理客户端RPC调用包。
// 参数：client - 客户端连接，pkg - 调用包。
func (s *Server) handleRPCCallPackage(client *ClientConnection, pkg api.MajulaPackage) {
	clientInvokeId := pkg.InvokeId
	fun := pkg.Fun
	args := pkg.Args
	fromClientId := client.ID

	targetNode, ok1 := args["_target_node"].(string)
	provider, ok2 := args["_provider"].(string)

	if !ok1 || !ok2 {
		errInfo := "missing target_node or provider in RPC call"
		Error("错误信息", "错误=", errInfo)
		client.SendCh <- api.MajulaPackage{
			Method:   "RPC_RESULT",
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
				Log("发送成功", "来源客户端=", fromClientId, "目标节点=", targetNode)
			default:
				Error("客户端发送通道已满", "客户端ID=", fromClientId)
			}
		}
	}()
}

// 处理FRP注册包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleFRPRegisterPackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	localAddr, ok2 := pkg.Args["local_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)
	remoteAddr, ok4 := pkg.Args["remote_addr"].(string)

	if !ok1 || !ok2 || !ok3 || !ok4 {
		Error("FRP注册缺少参数")
		return
	}

	err := s.Node.StubManager.RegisterFRPWithCode(code, localAddr, remoteNode, remoteAddr)
	if err != nil {
		Error("FRP注册失败", "错误=", err)
	}
}

// 处理FRP通过地址注册包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleFRPRegisterWithAddrPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	remoteAddr, ok2 := pkg.Args["remote_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)
	if !ok1 || !ok2 || !ok3 {
		Error("FRP注册缺少参数")
	}

	_, err := s.Node.StubManager.RegisterFRPWithoutCode(localAddr, remoteNode, remoteAddr)
	if err != nil {
		Error("FRP注册失败", "错误=", err)
	}
}

// 处理FRP双向注册包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleFRPRegisterTwoSidePackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	targetAddr, ok3 := pkg.Args["target_addr"].(string)
	isServer, ok4 := pkg.Args["is_server"].(bool)

	if !ok1 || !ok2 || !ok3 || !ok4 {
		Error("FRP双向注册缺少参数")
		return
	}
	err := s.Node.StubManager.RegisterFRPSimplified(code, remoteNode, targetAddr, isServer)
	if err != nil {
		Error("FRP注册失败", "错误=", err)
	}
}

// 处理启动已注册FRP监听包。
// 参数：client - 客户端连接，pkg - 启动包。
func (s *Server) handleStartFRPWithRegistrationPackage(client *ClientConnection, pkg api.MajulaPackage) {
	code, ok1 := pkg.Args["code"].(string)
	if !ok1 {
		Error("FRP启动缺少参数")
		return
	}
	err := s.Node.StubManager.RunFRPDynamicWithRegistration(code)
	if err != nil {
		Error("FRP启动失败", "错误=", err)
	}
}

// 处理启动FRP监听（无需注册）包。
// 参数：client - 客户端连接，pkg - 启动包。
func (s *Server) handleStartFRPWithoutRegistrationPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	remoteAddr, ok2 := pkg.Args["remote_addr"].(string)
	remoteNode, ok3 := pkg.Args["remote_node"].(string)

	if !ok1 || !ok2 || !ok3 {
		Error("FRP启动缺少参数")
		return
	}

	_, err := s.Node.StubManager.RegisterFRPWithoutCode(localAddr, remoteNode, remoteAddr)
	if err != nil {
		Error("FRP注册失败")
		return
	}
	err = s.Node.StubManager.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
	if err != nil {
		Error("FRP启动失败")
		return
	}
}

// 处理通过本地地址启动FRP监听包。
// 参数：client - 客户端连接，pkg - 启动包。
func (s *Server) handleStartFRPWithLocalAddressPackage(client *ClientConnection, pkg api.MajulaPackage) {
	localAddr, ok1 := pkg.Args["local_addr"].(string)
	if !ok1 {
		Error("FRP启动缺少参数")
		return
	}

	err := s.Node.StubManager.RunFRPDynamicWithRegistrationLocalAddr(localAddr)
	if err != nil {
		Error("FRP启动失败")
		return
	}
}

// 处理注册并运行Nginx FRP包。
// 参数：client - 客户端连接，pkg - 注册包。
func (s *Server) handleRegisterNginxFRPAndRunPackage(client *ClientConnection, pkg api.MajulaPackage) {
	var extraArgs map[string]string
	extraRaw, ok := pkg.Args["extra_args"].(string)
	if !ok {
		Error("Nginx FRP参数错误")
		return
	}
	err := json.Unmarshal([]byte(extraRaw), &extraArgs)
	if err != nil {
		Error("解析额外参数失败", "错误=", err)
		return
	}

	mappedAddr, ok1 := pkg.Args["mapped_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	hostAddr, ok3 := pkg.Args["remote_url"].(string)
	if !ok1 || !ok2 || !ok3 {
		Error("Nginx FRP参数错误")
		return
	}
	err = s.RegisterNginxFrp(mappedAddr, remoteNode, hostAddr, extraArgs)
	if err != nil {
		Error("注册并运行Nginx FRP失败", "错误=", err)
		return
	}
}

// 处理移除Nginx FRP包。
// 参数：client - 客户端连接，pkg - 注销包。
func (s *Server) handleUnregisterNginxFRPPackage(client *ClientConnection, pkg api.MajulaPackage) {
	var extraArgs map[string]string
	extraRaw, ok := pkg.Args["extra_args"].(string)
	if !ok {
		Error("Nginx FRP参数错误")
		return
	}
	err := json.Unmarshal([]byte(extraRaw), &extraArgs)
	if err != nil {
		Error("解析额外参数失败", "错误=", err)
		return
	}

	mappedAddr, ok1 := pkg.Args["mapped_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	hostAddr, ok3 := pkg.Args["remote_url"].(string)
	if !ok1 || !ok2 || !ok3 {
		Error("Nginx FRP参数错误")
		return
	}
	err = s.RemoveNginxFrp(mappedAddr, remoteNode, hostAddr, extraArgs)
	if err != nil {
		Error("注册并运行Nginx FRP失败", "错误=", err)
		return
	}
}

// 处理向远程节点传输文件包。
// 参数：client - 客户端连接，pkg - 传输包。
func (s *Server) handleTransferFileToRemotePackage(client *ClientConnection, pkg api.MajulaPackage) {
	localPath, ok1 := pkg.Args["local_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	remotePath, ok3 := pkg.Args["remote_path"].(string)
	if !ok1 || !ok2 || !ok3 {
		Error("FRP传输缺少参数")
		return
	}
	err := s.Node.StubManager.TransferFileToRemoteWithoutRegistration(remoteNode, localPath, remotePath)
	if err != nil {
		Error("向远程传输文件失败", "错误=", err)
		return
	}
}

// 处理从远程节点下载文件包。
// 参数：client - 客户端连接，pkg - 下载包。
func (s *Server) handleDownloadFileFromRemotePackage(client *ClientConnection, pkg api.MajulaPackage) {
	localPath, ok1 := pkg.Args["local_path"].(string)
	remoteNode, ok2 := pkg.Args["remote_node"].(string)
	remotePath, ok3 := pkg.Args["remote_path"].(string)
	if !ok1 || !ok2 || !ok3 {
		Error("FRP下载文件缺少参数")
	}
	err := s.Node.StubManager.DownloadFileFromRemoteWithoutRegistration(remoteNode, remotePath, localPath)
	if err != nil {
		Error("从远程下载文件失败", "错误=", err)
		return
	}
}

// 统一处理所有客户端发来的包。
// 参数：client - 客户端连接，pkg - 任意包。
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

	case "RETURN_RESULT":
		go s.handleReturnResultPackage(client, pkg)

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

	case "JOIN_RAFT_LEARNER":
		go s.handleJoinRaftLearnerPackage(client, pkg)
	case "JOIN_RAFT_LEARNER_WITH_FAST_SYNC":
		go s.handleJoinRaftLearnerWithFastSyncPackage(client, pkg)
	case "LEAVE_RAFT_LEARNER":
		go s.handleLeaveRaftLearnerPackage(client, pkg)

	case "CONNECT_TO_NODE":
		go s.handleConnectToNodePackage(client, pkg)

	case "JOIN_ELECTION":
		go s.handleJoinElectionPackage(client, pkg)
	case "GIVEUP_ELECTION":
		go s.handleGiveUpElectionPackage(client, pkg)
	case "LEAVE_ELECTION":
		go s.handleLeaveElectionPackage(client, pkg)
	case "GET_ELECTION_STATUS":
		go s.handleGetElectionStatusPackage(client, pkg)

	case "PUT_IN_GROUP":
		go s.handlePutInGroupPackage(client, pkg)

	case "DELETE_FROM_GROUP":
		go s.handleDeleteFromGroupPackage(client, pkg)

	case "GET_FROM_GROUP":
		go s.handleGetFromGroupPackage(client, pkg)

	default:

	}
}

// handleJoinRaftLearnerPackage 处理客户端请求以 learner 形式加入 raft group
// 参数：client - 客户端连接，pkg - 指令包
func (s *Server) handleJoinRaftLearnerPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	dbPath, _ := pkg.Args["dbpath"].(string)
	if group == "" || dbPath == "" {
		return
	}
	s.Node.RaftManager.JoinAsLearner(group, s.Node, dbPath)
}

// handleJoinRaftLearnerWithFastSyncPackage 处理客户端请求以 learner 形式加入 raft group，使用快速同步
// 参数：client - 客户端连接，pkg - 指令包
func (s *Server) handleJoinRaftLearnerWithFastSyncPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	dbPath, _ := pkg.Args["dbpath"].(string)
	if group == "" || dbPath == "" {
		return
	}

	// 使用快速同步方式加入
	_, err := s.Node.RaftManager.CreateLearnerWithFastSync(group, s.Node, dbPath)
	if err != nil {
		Error("Learner快速同步加入失败", "组名=", group, "错误=", err.Error())
	} else {
		Log("Learner快速同步加入成功", "组名=", group, "客户端ID=", client.ID)
	}
}

// handleLeaveRaftLearnerPackage 处理客户端请求以 learner 形式退出 raft group
// 参数：client - 客户端连接，pkg - 指令包
func (s *Server) handleLeaveRaftLearnerPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	if group == "" {
		return
	}
	s.Node.RaftManager.LeaveAsLearner(group, s.Node)
}

// handleConnectToNodePackage 处理连接到指定节点的请求
func (s *Server) handleConnectToNodePackage(client *ClientConnection, pkg api.MajulaPackage) {
	targetNodeID, _ := pkg.Args["target_node_id"].(string)
	if targetNodeID == "" {
		// 发送错误响应
		response := api.MajulaPackage{
			Method:   "CONNECT_TO_NODE_RESPONSE",
			Result:   map[string]interface{}{"error": "missing target_node_id"},
			InvokeId: pkg.InvokeId,
		}
		s.SendToClient(client.ID, response)
		return
	}

	// 检查是否有信令客户端
	if s.Node.SignalingClient == nil {
		response := api.MajulaPackage{
			Method:   "CONNECT_TO_NODE_RESPONSE",
			Result:   map[string]interface{}{"error": "signaling client not available"},
			InvokeId: pkg.InvokeId,
		}
		s.SendToClient(client.ID, response)
		return
	}

	// 异步执行连接
	go func() {
		// 通过信令客户端请求KCP连接
		response, err := s.Node.SignalingClient.RequestKCPConnection(targetNodeID)

		var result map[string]interface{}
		if err != nil {
			result = map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}
		} else {
			result = map[string]interface{}{
				"success":     response.Success,
				"target_node": targetNodeID,
				"public_addr": response.PublicAddr,
				"error_msg":   response.ErrorMsg,
			}
		}

		// 发送响应
		responsePkg := api.MajulaPackage{
			Method:   "CONNECT_TO_NODE_RESPONSE",
			Result:   result,
			InvokeId: pkg.InvokeId,
		}
		s.SendToClient(client.ID, responsePkg)
	}()
}

// 向指定客户端发送消息。
// 参数：clientID - 客户端ID，pkg - 消息包。
// 返回：错误信息（如有）。
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

// 注销指定客户端的所有RPC服务。
// 参数：clientID - 客户端ID。
func (s *Server) UnregisterClientRpcServices(clientID string) {
	s.Node.RpcFuncsMutex.Lock()
	defer s.Node.RpcFuncsMutex.Unlock()

	for funcName, providers := range s.Node.RpcFuncs {
		if _, ok := providers[clientID]; ok {
			delete(providers, clientID)
			Error("未注册的RPC", "函数名=", funcName, "客户端ID=", clientID)
		}
		if len(providers) == 0 {
			delete(s.Node.RpcFuncs, funcName)
		}
	}
}

// 优雅关闭Server，断开所有客户端。
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

// 处理HTTP请求。
// 参数：c - Gin上下文。
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

	Log("HTTP客户端已连接", "客户端ID=", clientID)

	for {
		select {
		case <-ctx.Done():
			Log("HTTP客户端已断开", "客户端ID=", clientID)
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

// 注册支持GET和POST的路由。
// 参数：rg - 路由组，path - 路径，handler - 处理函数，withTarget - 是否带目标参数。
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

// 生成新的客户端ID。
// 返回：客户端ID字符串。
func (s *Server) generateClientID() string {
	id := atomic.AddInt64(&s.ClientCounter, 1)
	return fmt.Sprintf("client-%d", id)
}

// 处理订阅请求。
// 参数：c - Gin上下文。
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
		Log("临时客户端已取消订阅并移除", "客户端ID=", clientID)
	}()

	s.Node.addLocalSub(topic, clientID, func(topic, from, to string, content []byte) {
		var args map[string]interface{}
		_ = common.UnmarshalAny(content, &args)
		select {
		case client.SendCh <- api.MajulaPackage{
			Method: "PUBLISH",
			Topic:  topic,
			Args:   args,
		}:
		default:
			Error("客户端发送缓冲区已满，消息被丢弃", "客户端ID=", clientID)
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

	Log("临时客户端已订阅主题", "客户端ID=", clientID, "主题=", topic)

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

// 处理发布请求。
// 参数：c - Gin上下文。
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
		Log("客户端发布后已移除", "客户端ID=", clientID)
	}()

	s.Node.publishOnTopic(topic, message)

	c.JSON(http.StatusOK, gin.H{
		"status":  "published",
		"topic":   topic,
		"client":  clientID,
		"message": message,
	})
}

// 处理RPC请求。
// 参数：c - Gin上下文。
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
		Log("临时客户端RPC后已移除", "客户端ID=", clientID)
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

// 处理发送请求。
// 参数：c - Gin上下文。
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
		Log("临时客户端发送后已移除", "客户端ID=", clientID)
	}()

	payload := map[string]interface{}{
		"target_client": targetClient,
		"payload":       msg,
	}

	dataBytes, _ := common.MarshalAny(payload)

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

// 处理列出RPC服务请求。
// 参数：c - Gin上下文。
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
		Log("临时客户端列出RPC后已移除", "客户端ID=", clientID)
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

// Gin中间件，处理跨域请求。
// 返回：gin.HandlerFunc。
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

// 解析Gin参数。
// 参数：c - Gin上下文。
// 返回：目标字符串和参数map。
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

// 处理FRP相关HTTP请求。
// 参数：c - Gin上下文。
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

// 处理文件上传请求。
// 参数：c - Gin上下文。
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
			Error("动态文件上传错误", "错误=", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":      "upload dispatched",
		"remote_node": remoteNode,
		"local_path":  localPath,
		"remote_path": remotePath,
	})
}

// 处理文件下载请求。
// 参数：c - Gin上下文。
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
			Error("动态文件下载错误", "错误=", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":      "download dispatched",
		"remote_node": remoteNode,
		"remote_path": remotePath,
		"local_path":  localPath,
	})
}

func (s *Server) handleJoinRaftLearner(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	dbPath, _ := params["dbpath"].(string)
	if group == "" || dbPath == "" {
		c.JSON(400, gin.H{"error": "group 和 dbpath 不能为空"})
		return
	}
	err := s.Node.RaftManager.JoinAsLearner(group, s.Node, dbPath)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok", "group": group, "dbpath": dbPath})
}

func (s *Server) handleJoinRaftLearnerWithFastSync(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	dbPath, _ := params["dbpath"].(string)
	if group == "" || dbPath == "" {
		c.JSON(400, gin.H{"error": "group 和 dbpath 不能为空"})
		return
	}

	// 使用快速同步方式加入
	_, err := s.Node.RaftManager.CreateLearnerWithFastSync(group, s.Node, dbPath)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok", "group": group, "dbpath": dbPath, "method": "fast_sync"})
}

func (s *Server) handleLeaveRaftLearner(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	if group == "" {
		c.JSON(400, gin.H{"error": "group 不能为空"})
		return
	}
	err := s.Node.RaftManager.LeaveAsLearner(group, s.Node)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "ok", "group": group})
}

func (s *Server) handleConnectToNode(c *gin.Context) {
	_, params := parseGinParameters(c)
	targetNodeID, _ := params["target_node_id"].(string)
	if targetNodeID == "" {
		c.JSON(400, gin.H{"error": "target_node_id 不能为空"})
		return
	}

	// 检查是否有信令客户端
	if s.Node.SignalingClient == nil {
		c.JSON(500, gin.H{"error": "signaling client not available"})
		return
	}

	// 异步执行连接
	go func() {
		// 通过信令客户端请求KCP连接
		response, err := s.Node.SignalingClient.RequestKCPConnection(targetNodeID)

		if err != nil {
			Error("节点连接失败", "目标节点=", targetNodeID, "错误=", err.Error())
		} else if response.Success {
			Log("节点连接成功", "目标节点=", targetNodeID, "公网地址=", response.PublicAddr)
		} else {
			Error("节点连接失败", "目标节点=", targetNodeID, "错误=", response.ErrorMsg)
		}
	}()

	c.JSON(200, gin.H{
		"status":      "connecting",
		"target_node": targetNodeID,
		"message":     "Connection request sent, check logs for result",
	})
}

// 处理加入选举请求
func (s *Server) handleElectJoin(c *gin.Context) {
	_, params := parseGinParameters(c)
	groupName, _ := params["group"].(string)
	baseOvertimeT, _ := params["base_overtime_t"].(float64)

	if groupName == "" {
		c.JSON(400, gin.H{"error": "group 不能为空"})
		return
	}

	if baseOvertimeT == 0 {
		baseOvertimeT = 3000
	}

	config := ElectConfig{
		BaseOvertimeT: int64(baseOvertimeT),
		GroupName:     groupName,
	}

	candidate, err := s.Node.ElectManager.CreateCandidate(groupName, config)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	err = candidate.Start()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"status": "ok",
		"group":  groupName,
		"config": config,
	})
}

// 处理放弃选举请求
func (s *Server) handleElectGiveUp(c *gin.Context) {
	_, params := parseGinParameters(c)
	groupName, _ := params["group"].(string)

	if groupName == "" {
		c.JSON(400, gin.H{"error": "group 不能为空"})
		return
	}

	candidate, exists := s.Node.ElectManager.GetCandidate(groupName)
	if !exists {
		c.JSON(404, gin.H{"error": "选举组不存在"})
		return
	}

	candidate.GiveUp()

	c.JSON(200, gin.H{
		"status": "ok",
		"group":  groupName,
		"action": "giveup",
	})
}

// 处理退出选举请求
func (s *Server) handleElectLeave(c *gin.Context) {
	_, params := parseGinParameters(c)
	groupName, _ := params["group"].(string)

	if groupName == "" {
		c.JSON(400, gin.H{"error": "group 不能为空"})
		return
	}

	err := s.Node.ElectManager.RemoveCandidate(groupName)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"status": "ok",
		"group":  groupName,
		"action": "leave",
	})
}

// 处理选举状态查询请求
func (s *Server) handleElectStatus(c *gin.Context) {
	_, params := parseGinParameters(c)
	groupName, _ := params["group"].(string)

	if groupName == "" {
		stats := s.Node.ElectManager.GetStats()
		c.JSON(200, gin.H{
			"status": "ok",
			"stats":  stats,
		})
		return
	}

	candidate, exists := s.Node.ElectManager.GetCandidate(groupName)
	if !exists {
		c.JSON(404, gin.H{"error": "选举组不存在"})
		return
	}

	stats := candidate.GetStats()
	c.JSON(200, gin.H{
		"status": "ok",
		"group":  groupName,
		"stats":  stats,
	})
}

// handleJoinElectionPackage 处理客户端加入选举的WebSocket包
func (s *Server) handleJoinElectionPackage(client *ClientConnection, pkg api.MajulaPackage) {
	groupName, _ := pkg.Args["group"].(string)
	baseOvertimeT, _ := pkg.Args["base_overtime_t"].(float64)

	if groupName == "" {
		return
	}

	if baseOvertimeT == 0 {
		baseOvertimeT = 3000
	}

	config := ElectConfig{
		BaseOvertimeT: int64(baseOvertimeT),
		GroupName:     groupName,
	}

	candidate, err := s.Node.ElectManager.CreateCandidate(groupName, config)
	if err != nil {
		return
	}

	err = candidate.Start()
	if err != nil {
		return
	}
}

// handleGiveUpElectionPackage 处理客户端放弃选举的WebSocket包
func (s *Server) handleGiveUpElectionPackage(client *ClientConnection, pkg api.MajulaPackage) {
	groupName, _ := pkg.Args["group"].(string)

	if groupName == "" {
		return
	}

	candidate, exists := s.Node.ElectManager.GetCandidate(groupName)
	if !exists {
		return
	}

	candidate.GiveUp()
}

// handleLeaveElectionPackage 处理客户端退出选举的WebSocket包
func (s *Server) handleLeaveElectionPackage(client *ClientConnection, pkg api.MajulaPackage) {
	groupName, _ := pkg.Args["group"].(string)

	if groupName == "" {
		return
	}

	s.Node.ElectManager.RemoveCandidate(groupName)
}

// handleGetElectionStatusPackage 处理客户端获取选举状态的WebSocket包
func (s *Server) handleGetElectionStatusPackage(client *ClientConnection, pkg api.MajulaPackage) {
	groupName, _ := pkg.Args["group"].(string)

	var result interface{}
	if groupName == "" {
		result = s.Node.ElectManager.GetStats()
	} else {
		candidate, exists := s.Node.ElectManager.GetCandidate(groupName)
		if !exists {
			return
		}
		result = candidate.GetStats()
	}

	response := api.MajulaPackage{
		Method:   "ELECTION_STATUS_RESULT",
		Result:   result,
		InvokeId: pkg.InvokeId,
	}
	s.SendToClient(client.ID, response)
}

// handlePutInGroupPackage 处理客户端请求向Raft组中写入键值对
func (s *Server) handlePutInGroupPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	key, _ := pkg.Args["key"].(string)
	value, _ := pkg.Args["value"]

	if group == "" || key == "" {
		return
	}

	// 通过RPC调用Raft的put操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "put", map[string]interface{}{
		"group": group,
		"key":   key,
		"value": value,
	})

	// 发送结果给客户端
	response := api.MajulaPackage{
		Method:   "PUT_IN_GROUP_RESPONSE",
		InvokeId: pkg.InvokeId,
		Result:   result,
	}
	if !ok {
		response.Result = map[string]interface{}{"error": "RPC call failed"}
	}
	s.SendToClient(client.ID, response)
}

// handleDeleteFromGroupPackage 处理客户端请求从Raft组中删除键
func (s *Server) handleDeleteFromGroupPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	key, _ := pkg.Args["key"].(string)

	if group == "" || key == "" {
		return
	}

	// 通过RPC调用Raft的delete操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "delete", map[string]interface{}{
		"group": group,
		"key":   key,
	})

	// 发送结果给客户端
	response := api.MajulaPackage{
		Method:   "DELETE_FROM_GROUP_RESPONSE",
		InvokeId: pkg.InvokeId,
		Result:   result,
	}
	if !ok {
		response.Result = map[string]interface{}{"error": "RPC call failed"}
	}
	s.SendToClient(client.ID, response)
}

// handleGetFromGroupPackage 处理客户端请求从Raft组中读取值
func (s *Server) handleGetFromGroupPackage(client *ClientConnection, pkg api.MajulaPackage) {
	group, _ := pkg.Args["group"].(string)
	key, _ := pkg.Args["key"].(string)

	if group == "" || key == "" {
		return
	}

	// 通过RPC调用Raft的get操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "get", map[string]interface{}{
		"group": group,
		"key":   key,
	})

	// 发送结果给客户端
	response := api.MajulaPackage{
		Method:   "GET_FROM_GROUP_RESPONSE",
		InvokeId: pkg.InvokeId,
		Result:   result,
	}
	if !ok {
		response.Result = map[string]interface{}{"error": "RPC call failed"}
	}
	s.SendToClient(client.ID, response)
}

// handleRaftPut HTTP处理器：向Raft组中写入键值对
func (s *Server) handleRaftPut(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	key, _ := params["key"].(string)
	value, _ := params["value"]

	if group == "" || key == "" {
		c.JSON(400, gin.H{"error": "group 和 key 不能为空"})
		return
	}

	// 通过RPC调用Raft的put操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "put", map[string]interface{}{
		"group": group,
		"key":   key,
		"value": value,
	})

	if !ok {
		c.JSON(500, gin.H{"error": "RPC调用失败"})
		return
	}

	// 检查结果
	if resultMap, ok := result.(map[string]interface{}); ok {
		if errorMsg, hasError := resultMap["error"].(string); hasError {
			c.JSON(500, gin.H{"error": errorMsg})
			return
		}
	}

	c.JSON(200, gin.H{
		"status": "ok",
		"result": result,
		"group":  group,
		"key":    key,
	})
}

// handleRaftDelete HTTP处理器：从Raft组中删除键
func (s *Server) handleRaftDelete(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	key, _ := params["key"].(string)

	if group == "" || key == "" {
		c.JSON(400, gin.H{"error": "group 和 key 不能为空"})
		return
	}

	// 通过RPC调用Raft的delete操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "delete", map[string]interface{}{
		"group": group,
		"key":   key,
	})

	if !ok {
		c.JSON(500, gin.H{"error": "RPC调用失败"})
		return
	}

	// 检查结果
	if resultMap, ok := result.(map[string]interface{}); ok {
		if errorMsg, hasError := resultMap["error"].(string); hasError {
			c.JSON(500, gin.H{"error": errorMsg})
			return
		}
	}

	c.JSON(200, gin.H{
		"status": "ok",
		"result": result,
		"group":  group,
		"key":    key,
	})
}

// handleRaftGet HTTP处理器：从Raft组中读取值
func (s *Server) handleRaftGet(c *gin.Context) {
	_, params := parseGinParameters(c)
	group, _ := params["group"].(string)
	key, _ := params["key"].(string)

	if group == "" || key == "" {
		c.JSON(400, gin.H{"error": "group 和 key 不能为空"})
		return
	}

	// 通过RPC调用Raft的get操作
	result, ok := s.Node.MakeRpcRequest(s.Node.ID, "raft", "get", map[string]interface{}{
		"group": group,
		"key":   key,
	})

	if !ok {
		c.JSON(500, gin.H{"error": "RPC调用失败"})
		return
	}

	// 检查结果
	if resultMap, ok := result.(map[string]interface{}); ok {
		if errorMsg, hasError := resultMap["error"].(string); hasError {
			c.JSON(500, gin.H{"error": errorMsg})
			return
		}
	}

	c.JSON(200, gin.H{
		"status": "ok",
		"result": result,
		"group":  group,
		"key":    key,
	})
}
