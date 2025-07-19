package core

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
)

type HTTPProxyDefine struct {
	Schema     string            `json:"schema,omitempty"`
	HostAddr   string            `json:"host_addr,omitempty"`
	LocalAddr  string            `json:"local_addr,omitempty"`
	MappedAddr string            `json:"mapped_addr,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// SubKey 生成对应proxy的参数的subkey，用来更精确查找
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

// AddHttpProxy 添加一个httpProxy
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

// RemoveHttpProxy 删除一个对应的httpProxy
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

// FindHttpProxy 根据url查找最匹配的proxy
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

func WrapNginxFRPAddr(localAddr string) string {
	code := "_frp_" + localAddr
	return code
}

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

func (s *Server) RegisterNginxFrp(mappedPath, remoteNode, remoteBaseUrl string, extraArgs map[string]string) error {
	form := url.Values{}
	form.Set("action", "start")
	form.Set("addr_", remoteNode)
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

func (s *Server) ListNginxFrp() ([]map[string]interface{}, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%s/majula/map?action=list", s.Port)
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
