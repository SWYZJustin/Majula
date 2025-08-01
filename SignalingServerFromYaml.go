package main

import (
	"Majula/server"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type SignalingServerConfigYaml struct {
	Server struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
	} `yaml:"server"`

	Network struct {
		HTTPPort int    `yaml:"http_port"`
		UDPPort  int    `yaml:"udp_port"`
		Host     string `yaml:"host"`
	} `yaml:"network"`

	WebSocket struct {
		ReadTimeout    string `yaml:"read_timeout"`
		WriteTimeout   string `yaml:"write_timeout"`
		PingInterval   string `yaml:"ping_interval"`
		PongWait       string `yaml:"pong_wait"`
		MaxMessageSize int64  `yaml:"max_message_size"`
	} `yaml:"websocket"`

	NodeManagement struct {
		CleanupInterval string `yaml:"cleanup_interval"`
		OfflineTimeout  string `yaml:"offline_timeout"`
		MaxNodes        int    `yaml:"max_nodes"`
	} `yaml:"node_management"`
}

func parseSignalingDuration(durationStr string) time.Duration {
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		log.Printf("Warning: failed to parse duration '%s', using default: %v", durationStr, err)
		return 60 * time.Second
	}
	return duration
}

func validateSignalingConfig(conf *SignalingServerConfigYaml) error {
	if conf.Network.HTTPPort == 0 {
		return fmt.Errorf("network.http_port 不能为空")
	}
	if conf.Network.UDPPort == 0 {
		return fmt.Errorf("network.udp_port 不能为空")
	}
	return nil
}

func main() {
	configFile := "SignalingServerTemplate.yaml"
	if len(os.Args) > 1 {
		configFile = os.Args[1]
	}

	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		log.Fatalf("读取配置文件失败: %v", err)
	}

	var conf SignalingServerConfigYaml
	err = yaml.Unmarshal(data, &conf)
	if err != nil {
		log.Fatalf("解析YAML失败: %v", err)
	}

	if err := validateSignalingConfig(&conf); err != nil {
		log.Fatalf("配置文件校验失败: %v", err)
	}

	fmt.Printf("加载配置: %+v\n", conf)

	// 创建信令服务器配置
	serverConfig := &server.SignalingConfig{
		Port:            conf.Network.HTTPPort,
		UDPPort:         conf.Network.UDPPort,
		ReadTimeout:     parseSignalingDuration(conf.WebSocket.ReadTimeout),
		WriteTimeout:    parseSignalingDuration(conf.WebSocket.WriteTimeout),
		PingInterval:    parseSignalingDuration(conf.WebSocket.PingInterval),
		PongWait:        parseSignalingDuration(conf.WebSocket.PongWait),
		MaxMessageSize:  conf.WebSocket.MaxMessageSize,
		CleanupInterval: parseSignalingDuration(conf.NodeManagement.CleanupInterval),
	}

	// 创建信令服务器
	signalingServer := server.NewSignalingServer(serverConfig)

	fmt.Printf("信令服务器正在启动，HTTP端口: %d, UDP端口: %d\n", conf.Network.HTTPPort, conf.Network.UDPPort)

	// 启动服务器
	if err := signalingServer.Start(); err != nil {
		log.Fatalf("启动信令服务器失败: %v", err)
	}
}
