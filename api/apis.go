package api

import "time"

// Client 是用户主要交互的 API 接口。
// 封装了底层 MajulaClient 的所有功能，提供更简洁的使用方式。
type Client struct {
	inner *MajulaClient
}

// NewClient 创建并连接一个新的 WebSocket 客户端。
//
// addr:   服务端地址（例如 "wss://your-node.example.com"）
// entity: 客户端标识（例如 "user-abc" 或 "device-123"）
func NewClient(addr, entity string) *Client {
	return &Client{
		inner: NewMajulaClient(addr, entity),
	}
}

// CallRpc 执行一次远程 RPC 请求。
//
// fun:       要调用的函数名（例如 "getUserInfo"）
// args:      函数参数（key-value 形式）
// targetNode: 目标节点 ID（函数在哪个节点提供）
// provider:  提供该函数的服务名
// timeout:   超时时间
//
// 返回值为调用结果与是否成功（true 表示成功，false 表示超时）
func (c *Client) CallRpc(fun string, args map[string]interface{}, targetNode, provider string, timeout time.Duration) (interface{}, bool) {
	return c.inner.CallRpc(fun, targetNode, provider, args, timeout)
}

// RegisterRpc 注册一个本地 RPC 函数，允许其他节点调用。
//
// fun:     函数名
// handler: 本地函数处理器
// meta:    可选的函数说明元信息（用于文档或自动生成）
func (c *Client) RegisterRpc(fun string, handler RpcCallback, meta *RpcMeta) {
	c.inner.RegisterRpc(fun, handler, meta)
}

// CallRpcAsync 异步执行一次远程 RPC 请求（非阻塞）。
//
// fun:        要调用的函数名（例如 "getUserInfo"）
// args:       函数参数（key-value 形式）
// targetNode: 目标节点 ID（函数在哪个节点提供）
// provider:   提供该函数的服务名
// timeout:    超时时间
// callback:   调用完成后的回调函数（第一个参数为结果，第二个为是否成功）
//
// 注意：调用结果不会立即返回，而是异步通过回调函数通知调用方。
// 常用于不希望阻塞主流程的场景，如 UI、批处理、日志写入等。
func (c *Client) CallRpcAsync(fun string, args map[string]interface{}, targetNode, provider string, timeout time.Duration, callback func(interface{}, bool)) {
	c.inner.CallRpcAsync(fun, targetNode, provider, args, timeout, callback)
}

// UnregisterRpc 注销一个已注册的本地 RPC 函数。
//
// fun: 要注销的函数名
func (c *Client) UnregisterRpc(fun string) {
	c.inner.UnregisterRpc(fun)
}

// Subscribe 订阅一个主题，用于接收实时消息。
//
// topic:   要订阅的主题名
// handler: 收到该主题消息时的回调函数
func (c *Client) Subscribe(topic string, handler SubCallback) {
	c.inner.Subscribe(topic, handler)
}

// Unsubscribe 取消订阅某个主题。
//
// topic: 要取消订阅的主题名
func (c *Client) Unsubscribe(topic string) {
	c.inner.Unsubscribe(topic)
}

// Publish 向某个主题发布一条消息，发送给所有订阅者。
//
// topic: 要发布的主题名
// args:  消息内容（必须为可序列化的 map）
func (c *Client) Publish(topic string, args map[string]interface{}) {
	c.inner.Publish(topic, args)
}

// OnPrivate 设置一个私有消息接收处理函数，用于接收点对点消息。
//
// handler: 当收到私有消息时的处理函数
func (c *Client) OnPrivate(handler SubCallback) {
	c.inner.OnPrivateMessage(handler)
}

// SendPrivate 向指定节点的某个客户端发送私有消息（点对点消息）。
//
// targetNode:   目标节点 ID
// targetClient: 目标客户端 ID
// payload:      消息内容
func (c *Client) SendPrivate(targetNode, targetClient string, payload map[string]interface{}) {
	c.inner.SendPrivateMessage(targetNode, targetClient, payload)
}

// RegisterFRP 注册一个基于 code 的 FRP 隧道映射。用于将本地服务通过远程节点暴露出来。
//
// code:        注册标识符（用于标记此隧道）
// localAddr:   本地监听地址（如 "127.0.0.1:8080"）
// remoteNode:  远端节点 ID
// remoteAddr:  远端服务映射地址（如 "0.0.0.0:80"）
func (c *Client) RegisterFRP(code, localAddr, remoteNode, remoteAddr string) {
	c.inner.RegisterFRP(code, localAddr, remoteAddr, remoteNode)
}

// RegisterFRPWithAddr 注册一个基于地址的 FRP 隧道。
//
// localAddr: 本地监听地址
// remoteNode: 对端节点 ID
// remoteAddr: 对端目标地址
func (c *Client) RegisterFRPWithAddr(localAddr, remoteNode, remoteAddr string) {
	c.inner.RegisterFRPWithAddr(localAddr, remoteNode, remoteAddr)
}

// StartFRPListener 启动一个已注册 code 的 FRP 隧道监听服务。
//
// code: 注册时使用的唯一标识符
func (c *Client) StartFRPListener(code string) {
	c.inner.StartFRPWithRegistration(code)
}

// StartFRPWithLocalAddr 启动一个基于本地地址的 FRP 监听服务。
//
// localAddr: 要监听的本地地址
func (c *Client) StartFRPWithLocalAddr(localAddr string) {
	c.inner.StartFRPWithLocalAddr(localAddr)
}

// StartFRPWithoutRegistration 启动无需注册的动态 FRP 监听。
//
// localAddr: 本地监听地址
// remoteNode: 对端节点 ID
// remoteAddr: 对端目标地址
func (c *Client) StartFRPWithoutRegistration(localAddr, remoteNode, remoteAddr string) {
	c.inner.StartFRPWithoutRegistration(localAddr, remoteNode, remoteAddr)
}

// RegisterNginxFRP 注册并运行一个 NGINX HTTP 反向代理映射。
// 用于暴露本地 Web 服务。
//
// mappedPath: 映射路径（如 "/foo"）
// remoteNode: 对端节点 ID
// remoteURL: 目标 HTTP 地址
// extraArgs: 额外参数（如 header、method 等）
func (c *Client) RegisterNginxFRP(mappedPath, remoteNode, remoteURL string, extraArgs map[string]string) {
	c.inner.RegisterNginxFRPAndRun(mappedPath, remoteNode, remoteURL, extraArgs)
}

// RemoveNginxFRP 移除一个已注册的 NGINX 映射代理。
//
// mappedPath: 映射路径
// remoteNode: 对端节点 ID
// remoteURL: 目标 HTTP 地址
// extraArgs: 额外参数（如 header、method 等）
func (c *Client) RemoveNginxFRP(mappedPath, remoteNode, remoteURL string, extraArgs map[string]string) {
	c.inner.RemoveNginxFRP(mappedPath, remoteNode, remoteURL, extraArgs)
}

// UploadFile 上传本地文件至远程节点。
//
// remoteNode: 目标节点 ID
// localPath:  本地文件路径
// remotePath: 远程文件存储路径
func (c *Client) UploadFile(remoteNode, localPath, remotePath string) {
	c.inner.TransferFileToRemote(remoteNode, localPath, remotePath)
}

// DownloadFile 从远程节点下载文件到本地。
//
// remoteNode: 目标节点 ID
// remotePath: 远程文件路径
// localPath:  本地存储路径
func (c *Client) DownloadFile(remoteNode, remotePath, localPath string) {
	c.inner.DownloadFileFromRemote(remoteNode, remotePath, localPath)
}

// Quit 关闭客户端连接，并停止后台协程。
func (c *Client) Quit() {
	c.inner.Quit()
}
