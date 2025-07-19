package main

import (
	"Majula/core"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"

	"gopkg.in/yaml.v3"
)

type TLSConfigYaml struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type TCPConfigYaml struct {
	FrameSize            int            `yaml:"frame_size"`
	InactiveSeconds      int64          `yaml:"inactive_seconds"`
	SendQueueSize        int            `yaml:"send_queue_size"`
	MaxConnectionsPerSec int            `yaml:"max_connections_per_sec"`
	IPWhitelist          []string       `yaml:"ip_whitelist"`
	TLS                  *TLSConfigYaml `yaml:"tls"`
}

type ChannelConfigYaml struct {
	Type       string        `yaml:"type"`
	ListenAddr string        `yaml:"listen_addr"`
	RemoteAddr string        `yaml:"remote_addr"`
	TCP        TCPConfigYaml `yaml:"tcp"`
}

type MajulaServerConfigYaml struct {
	Port int `yaml:"port"`
}

type NodeConfigYaml struct {
	NodeID        string                   `yaml:"node_id"`
	Token         string                   `yaml:"token"`
	MajulaServers []MajulaServerConfigYaml `yaml:"majula_servers"`
	Channels      []ChannelConfigYaml      `yaml:"channels"`
}

func buildTLSConfig(tlsConf *TLSConfigYaml) (*tls.Config, error) {
	if tlsConf == nil || tlsConf.CertFile == "" || tlsConf.KeyFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(tlsConf.CertFile, tlsConf.KeyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

func validateConfig(conf *NodeConfigYaml) error {
	if conf.NodeID == "" {
		return fmt.Errorf("配置文件缺少 node_id 字段")
	}
	if conf.Token == "" {
		return fmt.Errorf("配置文件缺少 token 字段")
	}
	if len(conf.MajulaServers) == 0 {
		return fmt.Errorf("majula_servers 至少要有一个")
	}
	for i, ms := range conf.MajulaServers {
		if ms.Port == 0 {
			return fmt.Errorf("majula_servers[%d] 的 port 不能为空", i)
		}
	}
	if len(conf.Channels) == 0 {
		return fmt.Errorf("channels 至少要有一个")
	}
	for i, ch := range conf.Channels {
		if ch.Type == "server" && ch.ListenAddr == "" {
			return fmt.Errorf("channels[%d] 的 listen_addr 不能为空", i)
		}
		if ch.Type == "client" && ch.RemoteAddr == "" {
			return fmt.Errorf("channels[%d] 的 remote_addr 不能为空", i)
		}
		if ch.TCP.FrameSize <= 0 {
			return fmt.Errorf("channels[%d] 的 tcp.frame_size 必须大于0", i)
		}
		if ch.TCP.InactiveSeconds <= 0 {
			return fmt.Errorf("channels[%d] 的 tcp.inactive_seconds 必须大于0", i)
		}
		if ch.TCP.SendQueueSize <= 0 {
			return fmt.Errorf("channels[%d] 的 tcp.send_queue_size 必须大于0", i)
		}
	}
	return nil
}

func main() {
	data, err := ioutil.ReadFile("MajulaNodeTemplate.yaml")
	if err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}
	var conf NodeConfigYaml
	err = yaml.Unmarshal(data, &conf)
	if err != nil {
		log.Fatalf("解析YAML失败: %v", err)
	}

	if err := validateConfig(&conf); err != nil {
		log.Fatalf("配置文件校验失败: %v", err)
	}

	fmt.Printf("加载配置: %+v\n", conf)

	node := core.NewNode(conf.NodeID)

	for _, ms := range conf.MajulaServers {
		go func(port int) {
			server := core.NewServer(node, fmt.Sprintf("%d", port))
			core.SetupRoutes(server).Run(fmt.Sprintf(":%d", port))
		}(ms.Port)
	}

	for _, ch := range conf.Channels {
		tlsConfig, err := buildTLSConfig(ch.TCP.TLS)
		if err != nil {
			log.Fatalf("加载TLS证书失败: %v", err)
		}
		if ch.Type == "server" {
			worker := core.NewTcpConnection(
				"_server_"+ch.ListenAddr, false, ch.ListenAddr, "", ch.TCP.IPWhitelist,
				ch.TCP.FrameSize, ch.TCP.InactiveSeconds, ch.TCP.SendQueueSize, ch.TCP.MaxConnectionsPerSec, tlsConfig, conf.Token,
			)
			if worker == nil {
				log.Fatalf("创建server通道失败: %s", ch.ListenAddr)
			}
			channel := core.NewChannelFull(ch.ListenAddr+"-channel", node, worker)
			worker.User = channel
			node.AddChannel(channel)
		} else if ch.Type == "client" {
			worker := core.NewTcpConnection(
				"_client_"+ch.RemoteAddr, true, "", ch.RemoteAddr, ch.TCP.IPWhitelist,
				ch.TCP.FrameSize, ch.TCP.InactiveSeconds, ch.TCP.SendQueueSize, ch.TCP.MaxConnectionsPerSec, tlsConfig, conf.Token,
			)
			if worker == nil {
				log.Fatalf("创建client通道失败: %s", ch.RemoteAddr)
			}
			channel := core.NewChannelFull(ch.RemoteAddr+"-channel", node, worker)
			worker.User = channel
			node.AddChannel(channel)
		}
	}

	fmt.Println("本地节点已根据配置文件启动。")
	node.Register()
	select {} // 阻塞主线程
}
