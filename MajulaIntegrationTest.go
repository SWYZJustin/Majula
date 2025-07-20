package main

import (
	"Majula/api"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// 等待WebSocket连接建立
func waitForConnection(client *api.MajulaClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if client.Connected {
			fmt.Printf("[INFO] %s WebSocket连接已建立\n", client.Entity)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("WebSocket连接超时: %s", client.Entity)
}

func main() {
	fmt.Println("Majula Client集成测试开始...")

	client1 := api.NewMajulaClient("http://127.0.0.1:18080", "client1")
	client2 := api.NewMajulaClient("http://127.0.0.1:18081", "client2")

	// 等待WebSocket连接建立
	fmt.Println("[INFO] 等待WebSocket连接建立...")
	if err := waitForConnection(client1, 10*time.Second); err != nil {
		log.Fatalf("[ERROR] client1连接失败: %v", err)
	}
	if err := waitForConnection(client2, 10*time.Second); err != nil {
		log.Fatalf("[ERROR] client2连接失败: %v", err)
	}

	// 等待节点间连接建立
	fmt.Println("[INFO] 等待节点间连接建立...")
	time.Sleep(3 * time.Second)

	// 注册RPC服务
	fmt.Println("[INFO] 注册RPC服务...")
	client1.RegisterRpc("echo", func(fun string, args map[string]interface{}) interface{} {
		return map[string]interface{}{"echo": args}
	}, nil)
	client2.RegisterRpc("add", func(fun string, args map[string]interface{}) interface{} {
		a, _ := args["a"].(float64)
		b, _ := args["b"].(float64)
		return map[string]interface{}{"sum": a + b}
	}, nil)

	// 启动一个本地 HTTP 服务供 NginxFrp 映射使用
	go func() {
		http.HandleFunc("/service1", func(w http.ResponseWriter, r *http.Request) {
			log.Printf("[Service1] %s %s", r.Method, r.URL.String())
			// 解析查询参数
			query := r.URL.Query()
			if len(query) == 0 {
				fmt.Fprintln(w, `{"msg":"from node2 backend"}`)
			} else {
				params := make(map[string][]string)
				for k, v := range query {
					params[k] = v
				}
				w.Header().Set("Content-Type", "application/json")
				jsonBytes, _ := json.MarshalIndent(params, "", "  ")
				w.Write(jsonBytes)
			}
		})

		err := http.ListenAndServe("0.0.0.0:8080", nil)
		if err != nil {
			log.Fatalf("[ERROR] 启动 node2 HTTP 服务失败: %v", err)
		}
	}()

	// 等待RPC注册完成
	time.Sleep(2 * time.Second)

	// 启动FRP监听
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

	// 启动FRP隧道
	fmt.Println("[INFO] 启动FRP隧道...")
	client1.StartFRPWithoutRegistration("127.0.0.1:23333", "node2", "127.0.0.1:23337")

	// 测试FRP数据转发
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
		fmt.Println("[FRP] 已向23333发送数据")
	}()

	// 注册NginxFrp代理
	fmt.Println("[INFO] 注册NginxFrp代理...")
	client1.RegisterNginxFRPAndRun("/service1", "node2", "http://0.0.0.0:8080", map[string]string{"a1": "hello", "a2": "newbe"})

	// 等待所有服务启动
	time.Sleep(3 * time.Second)

	// 等待服务启动稳定
	time.Sleep(2 * time.Second)

	// 验证 NginxFrp 是否生效
	fmt.Println("[TEST] 访问 NginxFrp 映射验证 HTTP 请求...")
	resp, err := http.Get("http://127.0.0.1:8080/service1?foo=123&bar=abc")
	if err != nil {
		log.Printf("[FAIL] NginxFrp 映射请求失败: %v", err)
	} else {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[PASS] NginxFrp 响应结果: %s\n", string(body))
	}

	// 测试RPC调用
	fmt.Println("[TEST] client1 调用 node2 的 add 服务...")
	res, ok := client1.CallRpc("add", "node2", "client2", map[string]interface{}{"a": 1, "b": 2}, time.Second*5)
	if !ok {
		log.Printf("[FAIL] client1 调用 node2 的 add 服务失败")
	} else {
		fmt.Printf("[PASS] client1 调用 node2 的 add 服务结果: %+v\n", res)
	}

	fmt.Println("[TEST] client2 调用 node1 的 echo 服务...")
	res2, ok2 := client2.CallRpc("echo", "node1", "client1", map[string]interface{}{"msg": "hello from client2"}, time.Second*5)
	if !ok2 {
		log.Printf("[FAIL] client2 调用 node1 的 echo 服务失败")
	} else {
		fmt.Printf("[PASS] client2 调用 node1 的 echo 服务结果: %+v\n", res2)
	}

	// 添加调试信息
	fmt.Println("[DEBUG] 等待一段时间查看详细日志...")
	time.Sleep(2 * time.Second)

	fmt.Println("[INFO] FRP和NginxFrp注册已完成，请手动用netcat/curl等工具测试端口转发和HTTP代理功能。")

	// 保持程序运行一段时间
	fmt.Println("[INFO] 保持程序运行10秒...")
	time.Sleep(10 * time.Second)

	// 优雅退出
	fmt.Println("[INFO] 开始退出...")
	client1.Quit()
	client2.Quit()

	fmt.Println("Majula Client集成测试结束！")
}
