package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// HTTPProxyDefine HTTPProxyDefine结构体，定义HTTP代理的相关参数。
type HTTPProxyDefine struct {
	Schema     string            `json:"schema,omitempty"`
	HostAddr   string            `json:"host_addr,omitempty"`
	LocalAddr  string            `json:"local_addr,omitempty"`
	MappedAddr string            `json:"mapped_addr,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// SubKey 生成代理的参数子键，用于精确查找。
// 返回：参数子键字符串。
func (h *HTTPProxyDefine) SubKey() string {
	if h.Args == nil || len(h.Args) == 0 {
		return ""
	}
	ks := []string{}
	for k, v := range h.Args {
		ks = append(ks, k+"="+v)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

// AddHttpProxy 添加一个HTTP代理配置。
// 参数：tpd - 代理定义。
// 返回：是否添加成功。
func (this *Node) AddHttpProxy(tpd HTTPProxyDefine) bool {
	this.HttpProxyStubsMutex.Lock()
	defer this.HttpProxyStubsMutex.Unlock()
	if _, ok := this.HttpProxyStubs[tpd.MappedAddr]; !ok {
		this.HttpProxyStubs[tpd.MappedAddr] = map[string]*HTTPProxyDefine{}
	}
	sbk := tpd.SubKey()
	if _, ok := this.HttpProxyStubs[tpd.MappedAddr][sbk]; !ok {
		this.HttpProxyStubs[tpd.MappedAddr][sbk] = &tpd
	} else {
		return false
	}
	return true
}

// RemoveHttpProxy 删除一个HTTP代理配置。
// 参数：tpd - 代理定义。
// 返回：被删除的代理定义指针。
func (this *Node) RemoveHttpProxy(tpd HTTPProxyDefine) *HTTPProxyDefine {
	this.HttpProxyStubsMutex.Lock()
	defer this.HttpProxyStubsMutex.Unlock()
	if _, ok := this.HttpProxyStubs[tpd.MappedAddr]; !ok {
		return nil
	}
	sbk := tpd.SubKey()
	stub, ok := this.HttpProxyStubs[tpd.MappedAddr][sbk]
	if !ok {
		return nil
	}
	delete(this.HttpProxyStubs[tpd.MappedAddr], sbk)
	return stub
}

// FindHttpProxy 根据URL查找最匹配的HTTP代理。
// 参数：url - 目标URL。
// 返回：最匹配的HTTP代理定义指针。
func (this *Node) FindHttpProxy(url *url.URL) *HTTPProxyDefine {
	this.HttpProxyStubsMutex.Lock()
	defer this.HttpProxyStubsMutex.Unlock()

	ss := strings.Split(url.EscapedPath(), "/")
	for i := len(ss); i > 0; i-- {
		prefix := strings.Join(ss[:i], "/")
		if stubs, ok := this.HttpProxyStubs[prefix]; ok {
			queryParams := url.Query()
			var retStub *HTTPProxyDefine
			args := -1
			for _, stub := range stubs {
				if stub.Args == nil || len(stub.Args) == 0 {
					if args >= 0 {
						continue
					}
					retStub = stub
					args = 0
				} else if args >= len(stub.Args) {
					continue
				} else {
					match := true
					for k, v := range stub.Args {
						actual := queryParams.Get(k)
						if v == "" {
							continue
						}
						if v[0] == '~' {
							if actual == v[1:] {
								match = false
								break
							}
						} else {
							if actual != v {
								match = false
								break
							}
						}
					}
					if match {
						retStub = stub
						args = len(stub.Args)
					}
				}
			}
			return retStub
		}
	}
	return nil
}

// ListHttpProxy 列出所有HTTP代理配置。
// 返回：HTTP代理定义切片。
func (this *Node) ListHttpProxy() []HTTPProxyDefine {
	this.HttpProxyStubsMutex.Lock()
	defer this.HttpProxyStubsMutex.Unlock()
	var result []HTTPProxyDefine
	for _, m := range this.HttpProxyStubs {
		for _, stub := range m {
			result = append(result, *stub)
		}
	}
	return result
}

// 查找可用的本地TCP监听端口。
// 参数：tryTimes - 尝试次数。
// 返回：可用端口号。
func findValidLocalTcpListenPort(tryTimes int) int {
	minPort, maxPort := 30000, 40000
	for i := 0; i < tryTimes; i++ {
		port := rand.Intn(maxPort-minPort) + minPort
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			l.Close()
			return port
		}
	}
	return 0
}

// 处理Nginx FRP相关的HTTP请求。
// 参数：c - Gin上下文。
func (s *Server) handleNginxFrp(c *gin.Context) {
	action, args := parseGinParameters(c)

	params := HTTPProxyDefine{Args: make(map[string]string)}

	remoteNode, _ := args["remote_node"].(string)
	delete(args, "remote_node")

	proxyUrl, _ := args["url"].(string)
	delete(args, "url")

	for k, v := range args {
		params.Args[k] = fmt.Sprintf("%v", v)
	}

	if action != "list" {
		if remoteNode == "" {
			c.JSONP(http.StatusBadRequest, "missing remote_node (remote Node id)")
			return
		}

		u, err := url.Parse(proxyUrl)
		if err != nil {
			c.JSONP(http.StatusBadRequest, fmt.Sprintf("invalid url: %v", err))
			return
		}
		params.Schema = u.Scheme
		params.MappedAddr = u.EscapedPath()

		remoteAddr := u.Hostname()
		if u.Port() == "" {
			if u.Scheme == "http" {
				remoteAddr += ":80"
			} else if u.Scheme == "https" {
				remoteAddr += ":443"
			} else {
				c.JSONP(http.StatusBadRequest, "unsupported scheme")
				return
			}
		} else {
			remoteAddr += ":" + u.Port()
		}
		params.HostAddr = remoteAddr

		port := findValidLocalTcpListenPort(100)
		if port == 0 {
			c.JSONP(http.StatusBadRequest, "no valid local Port")
			return
		}
		localAddr := fmt.Sprintf("127.0.0.1:%d", port)
		params.LocalAddr = localAddr

		if action == "start" {
			err := s.Node.NginxFRPStarter(localAddr, remoteNode, remoteAddr)
			if err != nil {
				c.JSONP(http.StatusBadRequest, fmt.Sprintf("failed to start FRP listener: %v", err))
				return
			}
			s.Node.AddHttpProxy(params)
			c.JSONP(http.StatusOK, gin.H{"ok": true, "local": localAddr})
			return
		} else if action == "stop" {
			stub := s.Node.RemoveHttpProxy(params)
			if stub == nil {
				c.JSONP(http.StatusBadRequest, "proxy not found")
				return
			}
			localStubAddr := stub.LocalAddr
			s.Node.StubManager.CloseStubByAddr(localStubAddr)
			c.JSONP(http.StatusOK, gin.H{"ok": true})
			return
		} else {
			c.JSONP(http.StatusBadRequest, "invalid action")
			return
		}
	} else {
		c.JSONP(http.StatusOK, s.Node.ListHttpProxy())
	}
}

// WrapNginxFRPAddr 包装Nginx FRP地址。
// 参数：localAddr - 本地地址。
// 返回：包装后的地址字符串。
func WrapNginxFRPAddr(localAddr string) string {
	code := "_frp_" + localAddr
	return code
}

// NginxFRPStarter 启动Nginx FRP服务。
// 参数：localAddr - 本地地址，remoteNode - 远程节点，remoteAddr - 远程地址。
// 返回：错误信息（如有）。
func (this *Node) NginxFRPStarter(localAddr, remoteNode, remoteAddr string) error {

	code := WrapNginxFRPAddr(localAddr)

	err := this.StubManager.RegisterFRP(&FRPConfig{
		Code:       code,
		LocalAddr:  localAddr,
		RemoteNode: remoteNode,
		RemoteAddr: remoteAddr,
	})
	if err != nil {
		return fmt.Errorf("failed to Register FRP: %v", err)
	}

	err = this.StubManager.RunFRPDynamicWithoutRegistration(localAddr, remoteNode, remoteAddr)
	if err != nil {
		return fmt.Errorf("failed to start dynamic FRP listener: %v", err)
	}

	return nil
}

// ReverseProxy Gin中间件，实现反向代理。
// 返回：gin.HandlerFunc。
func (s *Server) ReverseProxy() gin.HandlerFunc {
	return func(c *gin.Context) {
		hpd := s.Node.FindHttpProxy(c.Request.URL)
		if hpd == nil {
			c.Next()
			return
		}

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.Host = hpd.LocalAddr
				req.URL.Host = hpd.LocalAddr
				req.URL.Scheme = hpd.Schema

				query := req.URL.Query()
				for k, v := range hpd.Args {
					query.Set(k, v)
				}
				req.URL.RawQuery = query.Encode()

				Debug("反向代理最终请求", "方法=", req.Method, "协议=", req.URL.Scheme, "主机=", req.URL.Host, "路径=", req.URL.Path, "查询=", req.URL.RawQuery)

			},
			ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
				rw.WriteHeader(http.StatusBadGateway)
			},
		}

		c.Writer.Header().Set("Cache-Control", "no-store, must-revalidate")
		c.Writer.Header().Set("Pragma", "no-cache")
		c.Writer.Header().Set("Expires", "0")

		proxy.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

/*
func (s *Server) ReverseProxy() gin.HandlerFunc {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			hpd := s.Node.FindHttpProxy(req.URL)
			if hpd != nil {
				req.Host = hpd.LocalAddr
				req.URL.Host = hpd.LocalAddr
				req.URL.Scheme = hpd.Schema

				queryValues, _ := url.ParseQuery(req.URL.RawQuery)
				req.URL.RawQuery = queryValues.Encode()
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			rw.WriteHeader(http.StatusBadGateway)
		},
	}
	return func(c *gin.Context) {
		c.Writer.Header().Set("Cache-Control", "no-store, must-revalidate")
		c.Writer.Header().Set("Pragma", "no-cache")
		c.Writer.Header().Set("Expires", "0")

		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

*/

/*
func (s *Server) RegisterNginxFrp(mappedPath, remoteNode, remoteBaseUrl string, extraArgs map[string]string) error {
	form := url.Values{}
	form.Set("action", "start")
	form.Set("remote_node", remoteNode)
	form.Set("url", remoteBaseUrl+mappedPath)
	for k, v := range extraArgs {
		form.Set(k, v)
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map", s.Port)
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("FRP registration failed: %s", string(body))
	}

	log.Printf("FRP registered: mappedPath %s => %s", mappedPath, remoteBaseUrl)
	return nil
}

*/

// RegisterNginxFrp 注册Nginx FRP。
// 参数：mappedPath - 映射路径，remoteNode - 远程节点，remoteBaseUrl - 远程基础URL，extraArgs - 额外参数。
// 返回：错误信息（如有）。
func (s *Server) RegisterNginxFrp(mappedPath, remoteNode, remoteBaseUrl string, extraArgs map[string]string) error {
	// 构造JSON请求体
	payload := map[string]string{
		"remote_node": remoteNode,
		"url":         remoteBaseUrl + mappedPath,
	}
	// 合并额外参数
	for k, v := range extraArgs {
		payload[k] = v
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map/start", s.Port)
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("FRP registration failed: %s", string(body))
	}

	Log("FRP已注册", "映射路径=", mappedPath, "远程基础URL=", remoteBaseUrl)
	return nil
}

/*
func (s *Server) RemoveNginxFrp(mappedPath, remoteNode, remoteBaseUrl string, extraArgs map[string]string) error {
	form := url.Values{}
	form.Set("action", "stop")
	form.Set("remote_node", remoteNode)
	form.Set("url", remoteBaseUrl+mappedPath)
	for k, v := range extraArgs {
		form.Set(k, v)
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map", s.Port)
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("FRP removal failed: %s", string(body))
	}

	log.Printf("FRP removed: mappedPath %s => %s", mappedPath, remoteBaseUrl)
	return nil
}

*/

// RemoveNginxFrp 移除Nginx FRP。
// 参数：mappedPath - 映射路径，remoteNode - 远程节点，remoteBaseUrl - 远程基础URL，extraArgs - 额外参数。
// 返回：错误信息（如有）。
func (s *Server) RemoveNginxFrp(mappedPath, remoteNode, remoteBaseUrl string, extraArgs map[string]string) error {
	payload := map[string]string{
		"remote_node": remoteNode,
		"url":         remoteBaseUrl + mappedPath,
	}
	for k, v := range extraArgs {
		payload[k] = v
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map/stop", s.Port)
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("FRP removal failed: %s", string(body))
	}

	Log("FRP已移除", "映射路径=", mappedPath, "远程基础URL=", remoteBaseUrl)
	return nil
}

// ListNginxFrp 列出所有Nginx FRP配置。
// 返回：配置列表和错误信息。
func (s *Server) ListNginxFrp() ([]map[string]interface{}, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map/list", s.Port)
	resp, err := http.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := ioutil.ReadAll(resp.Body)

	var result []map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("response parse failed: %v", err)
	}

	return result, nil
}
