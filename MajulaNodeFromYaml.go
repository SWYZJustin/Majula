package main

import (
	"Majula/core"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"log"
	"os"

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

type KCPConfigYaml struct {
	FrameSize            int      `yaml:"frame_size"`
	InactiveSeconds      int64    `yaml:"inactive_seconds"`
	SendQueueSize        int      `yaml:"send_queue_size"`
	MaxConnectionsPerSec int      `yaml:"max_connections_per_sec"`
	IPWhitelist          []string `yaml:"ip_whitelist"`
}

type ChannelConfigYaml struct {
	Type       string        `yaml:"type"`
	Protocol   string        `yaml:"protocol"`
	ListenAddr string        `yaml:"listen_addr"`
	RemoteAddr string        `yaml:"remote_addr"`
	TCP        TCPConfigYaml `yaml:"tcp"`
	KCP        KCPConfigYaml `yaml:"kcp"`
}

type MajulaServerConfigYaml struct {
	Port int `yaml:"port"`
}

type RaftConfigYaml struct {
	Group  string   `yaml:"group"`
	Peers  []string `yaml:"peers"`
	DBPath string   `yaml:"dbpath"`
}

type NodeConfigYaml struct {
	NodeID        string                   `yaml:"node_id"`
	Token         string                   `yaml:"token"`
	MajulaServers []MajulaServerConfigYaml `yaml:"majula_servers"`
	Channels      []ChannelConfigYaml      `yaml:"channels"`
	Raft          []RaftConfigYaml         `yaml:"raft"`
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
		if ch.Protocol == "kcp" {
			if ch.KCP.FrameSize <= 0 {
				return fmt.Errorf("channels[%d] 的 kcp.frame_size 必须大于0", i)
			}
			if ch.KCP.InactiveSeconds <= 0 {
				return fmt.Errorf("channels[%d] 的 kcp.inactive_seconds 必须大于0", i)
			}
			if ch.KCP.SendQueueSize <= 0 {
				return fmt.Errorf("channels[%d] 的 kcp.send_queue_size 必须大于0", i)
			}
			if ch.TCP.TLS != nil {
				return fmt.Errorf("channels[%d] 的 kcp 配置下不能有 tls 字段", i)
			}
		} else { // 默认tcp
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
	}
	if len(conf.Raft) > 0 {
		for i, raftConf := range conf.Raft {
			if raftConf.Group == "" {
				return fmt.Errorf("raft[%d].group 不能为空", i)
			}
			if len(raftConf.Peers) == 0 {
				return fmt.Errorf("raft[%d].peers 至少要有一个", i)
			}
			if raftConf.DBPath == "" {
				return fmt.Errorf("raft[%d].dbpath 不能为空", i)
			}
		}
	}
	return nil
}

func main() {
	configFile := "MajulaNodeTemplate.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}
	data, err := ioutil.ReadFile(configFile)
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

	// Raft集群初始化
	if len(conf.Raft) > 0 {
		for _, raftConf := range conf.Raft {
			_, err := node.RaftManager.CreateRaftGroup(raftConf.Group, node, raftConf.Peers, raftConf.DBPath)
			if err != nil {
				log.Fatalf("Raft集群 %s 初始化失败: %v", raftConf.Group, err)
			}
			fmt.Printf("Raft集群 %s 初始化完成，核心节点: %v，dbpath: %s\n", raftConf.Group, raftConf.Peers, raftConf.DBPath)
		}
	}

	for _, ms := range conf.MajulaServers {
		go func(port int) {
			server := core.NewServer(node, fmt.Sprintf("%d", port))
			core.SetupRoutes(server).Run(fmt.Sprintf(":%d", port))
		}(ms.Port)
	}

	for _, ch := range conf.Channels {
		if ch.Protocol == "kcp" {
			// KCP通道
			if ch.Type == "server" {
				worker := core.NewKcpConnection(
					"_server_"+ch.ListenAddr, false, ch.ListenAddr, "", ch.KCP.IPWhitelist,
					ch.KCP.FrameSize, ch.KCP.InactiveSeconds, ch.KCP.SendQueueSize, ch.KCP.MaxConnectionsPerSec, conf.Token,
				)
				if worker == nil {
					log.Fatalf("创建KCP server通道失败: %s", ch.ListenAddr)
				}
				channel := core.NewChannelFull(ch.ListenAddr+"-channel", node, worker)
				worker.User = channel
				node.AddChannel(channel)
			} else if ch.Type == "client" {
				worker := core.NewKcpConnection(
					"_client_"+ch.RemoteAddr, true, "", ch.RemoteAddr, ch.KCP.IPWhitelist,
					ch.KCP.FrameSize, ch.KCP.InactiveSeconds, ch.KCP.SendQueueSize, ch.KCP.MaxConnectionsPerSec, conf.Token,
				)
				if worker == nil {
					log.Fatalf("创建KCP client通道失败: %s", ch.RemoteAddr)
				}
				channel := core.NewChannelFull(ch.RemoteAddr+"-channel", node, worker)
				worker.User = channel
				node.AddChannel(channel)
			}
		} else { // 默认tcp
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
	}

	fmt.Println("本地节点已根据配置文件启动。")
	node.Register()
	select {} // 阻塞主线程
}
