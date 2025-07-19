package main

import (
	"Majula/api"
	"fmt"
	"log"
	"net"
	"time"
)

func main() {
	fmt.Println("Majula Client集成测试开始...")

	client1 := api.NewMajulaClient("http://127.0.0.1:18080", "client1")
	client2 := api.NewMajulaClient("http://127.0.0.1:18081", "client2")

	time.Sleep(1 * time.Second)

	client1.RegisterClientID()
	client2.RegisterClientID()

	time.Sleep(1 * time.Second)

	client1.RegisterRpc("echo", func(fun string, args map[string]interface{}) interface{} {
		return map[string]interface{}{"echo": args}
	}, nil)
	client2.RegisterRpc("add", func(fun string, args map[string]interface{}) interface{} {
		a, _ := args["a"].(float64)
		b, _ := args["b"].(float64)
		return map[string]interface{}{"sum": a + b}
	}, nil)

	time.Sleep(1 * time.Second)
	go func() {
		ln, err := net.Listen("tcp", "127.0.0.1:23337")
		if err != nil {
			log.Fatalf("[FRP] 本地连接23337失败: %v", err)
		}
		fmt.Println("[FRP] 本地监听 127.0.0.1:23337 已启动，等待数据...")
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("[FRP] Accept失败: %v", err)
				continue
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					fmt.Printf("[FRP] node2 23337收到: %s\n", string(buf[:n]))
				}
			}(conn)
		}
	}()

	client1.RegisterFRP("test-frp", "127.0.0.1:23333", "node2", "127.0.0.1:23337")

	go func() {
		time.Sleep(4 * time.Second)
		conn, err := net.Dial("tcp", "127.0.0.1:23333")
		if err != nil {
			fmt.Println("[FRP] 连接23333失败:", err)
			return
		}
		defer conn.Close()
		msg := []byte("hello from node1 to node2 via FRP")
		conn.Write(msg)
		fmt.Println("[FRP] 已向23337发送数据")
	}()

	client1.RegisterNginxFRPAndRun("service1", "node2", "0.0.0.0:8080", map[string]string{"path": "/api", "rewrite": "/v1"})

	time.Sleep(2 * time.Second)

	fmt.Println("[TEST] client1 调用 node2 的 add 服务...")
	res, ok := client1.CallRpc("add", "node2", "default", map[string]interface{}{"a": 1, "b": 2}, time.Second*2)
	if !ok {
		log.Fatalf("[FAIL] client1 调用 node2 的 add 服务失败")
	}
	fmt.Printf("[PASS] client1 调用 node2 的 add 服务结果: %+v\n", res)

	fmt.Println("[TEST] client2 调用 node1 的 echo 服务...")
	res2, ok2 := client2.CallRpc("echo", "node1", "default", map[string]interface{}{"msg": "hello from client2"}, time.Second*2)
	if !ok2 {
		log.Fatalf("[FAIL] client2 调用 node1 的 echo 服务失败")
	}
	fmt.Printf("[PASS] client2 调用 node1 的 echo 服务结果: %+v\n", res2)

	fmt.Println("[INFO] FRP和NginxFrp注册已完成，请手动用netcat/curl等工具测试端口转发和HTTP代理功能。")

	go func() {

		client1.Quit()
		client2.Quit()
	}()

	fmt.Println("Majula Client集成测试结束！")
}
