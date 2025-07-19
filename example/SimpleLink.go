package main

import (
	"Majula/api"
	"Majula/core"
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// This Program Only server the purpose for test
// Cannot guarantee to run
// 仅作参考，不保证能跑

var env = core.NewEnv()
var activeClient *core.Client

func main() {
	reader := bufio.NewReader(os.Stdin)
	printGlobalHelp()

	for {
		if activeClient != nil {
			fmt.Printf("(%s)> ", activeClient.ClientId)
		} else {
			fmt.Print(">> ")
		}

		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		args := strings.Split(line, " ")

		// ========== CLIENT MODE ==========
		if activeClient != nil {
			switch args[0] {
			case "help":
				printClientHelp()
			case "exit":
				fmt.Printf("Exiting client session '%s'\n", activeClient.ClientId)
				activeClient = nil
			case "sub":
				if len(args) != 2 {
					fmt.Println("Usage: sub <topic>")
					continue
				}
				topic := args[1]
				err := activeClient.Subscribe(topic, func(topic, from, to string, content []byte) {
					fmt.Printf("[To %s] Topic: %s | From: %s | Message: %s\n", activeClient.ClientId, topic, from, string(content))
				})
				if err != nil {
					fmt.Println("Error:", err)
				} else {
					fmt.Println("Subscribed to topic:", topic)
				}
			case "unsub":
				if len(args) != 2 {
					fmt.Println("Usage: unsub <topic>")
					continue
				}
				err := activeClient.Unsubscribe(args[1])
				if err != nil {
					fmt.Println("Error:", err)
				} else {
					fmt.Println("Unsubscribed from topic:", args[1])
				}
			case "pub":
				if len(args) < 3 {
					fmt.Println("Usage: pub <topic> <message>")
					continue
				}
				topic := args[1]
				msg := strings.Join(args[2:], " ")
				err := activeClient.Publish(topic, msg)
				if err != nil {
					fmt.Println("Error:", err)
				} else {
					fmt.Println("Message published.")
				}

			case "sayhello":
				if len(args) != 2 {
					fmt.Println("Usage: sayhello <targetNodeID>")
					break
				}
				targetNodeID := args[1]
				if activeClient.TargetNode == nil {
					fmt.Println("Client is not connected to any node.")
					break
				}
				result, ok := activeClient.TargetNode.MakeRpcRequest(
					targetNodeID,
					"default",
					"whoami", // hardcoded function name
					map[string]interface{}{},
				)
				if !ok {
					fmt.Println("RPC call failed or timed out.")
				} else {
					fmt.Printf("Response from node '%s': %v\n", targetNodeID, result)
				}

			case "add":
				if len(args) != 3 {
					fmt.Println("Usage: add <targetNodeID> <a+b>  (example: add s2 4+5)")
					break
				}
				targetNodeID := args[1]
				input := args[2]
				parts := strings.Split(input, "+")
				if len(parts) != 2 {
					fmt.Println("Invalid format. Use: add <targetNodeID> <a+b>")
					break
				}
				var a, b float64
				_, err1 := fmt.Sscanf(parts[0], "%f", &a)
				_, err2 := fmt.Sscanf(parts[1], "%f", &b)
				if err1 != nil || err2 != nil {
					fmt.Println("Invalid numbers.")
					break
				}

				params := map[string]interface{}{
					"a": a,
					"b": b,
				}
				result, ok := activeClient.TargetNode.MakeRpcRequest(
					targetNodeID,
					"default",
					"add",
					params,
				)
				if !ok {
					fmt.Println("RPC call failed or timed out.")
				} else {
					if m, ok := result.(map[string]interface{}); ok {
						if errStr, hasErr := m["error"]; hasErr {
							fmt.Println("RPC Error:", errStr)
						} else if sum, ok := m["sum"]; ok {
							fmt.Printf("Sum from node '%s': %.2f\n", targetNodeID, sum)
						} else {
							fmt.Println("Unexpected response:", result)
						}
					} else {
						fmt.Println("RPC response:", result)
					}
				}

			default:
				fmt.Println("Unknown client command. Type 'help' for available commands.")
			}
			continue
		}

		// ========== GLOBAL MODE ==========
		switch args[0] {
		case "wlogin":
			if len(args) != 3 {
				fmt.Println("Usage: wlogin <clientId> <ws-url>")
				break
			}
			websocketConsoleSession(args[1], args[2])

		case "help":
			printGlobalHelp()
		case "start":
			if len(args) < 4 || len(args) > 5 {
				fmt.Println("Usage: start server|client <nodeID> <addr> [ws=<port>]")
				continue
			}
			role, nodeID, addr := args[1], args[2], args[3]

			wsPort := ""
			if len(args) == 5 && strings.HasPrefix(args[4], "ws=") {
				wsPort = strings.TrimPrefix(args[4], "ws=")
			}

			var err error
			if role == "server" {
				err = env.AddServer(nodeID, addr, wsPort)
			} else if role == "client" {
				err = env.AddClientNode(nodeID, addr, wsPort)
			} else {
				fmt.Println("Unknown role:", role)
				continue
			}
			if err != nil {
				fmt.Println("Error:", err)
			}

		case "addclient":
			if len(args) != 2 {
				fmt.Println("Usage: addclient <clientName>")
				continue
			}
			err := env.AddClient(args[1])
			if err != nil {
				fmt.Println("Error:", err)
			}
		case "connectclient":
			if len(args) != 3 {
				fmt.Println("Usage: connectclient <clientName> <nodeID>")
				continue
			}
			client := env.GetClient(args[1])
			node := env.GetNode(args[2])
			if client == nil {
				fmt.Println("Client not found.")
				continue
			}
			if node == nil {
				fmt.Println("Node not found.")
				continue
			}
			err := client.Connect(node)
			if err != nil {
				fmt.Println("Error:", err)
			} else {
				fmt.Printf("Client %s connected to node %s\n", client.ClientId, node.ID)
			}
		case "login":
			if len(args) != 2 {
				fmt.Println("Usage: login <clientName>")
				continue
			}
			client := env.GetClient(args[1])
			if client == nil {
				fmt.Println("Client not found.")
				continue
			}
			if client.TargetNode == nil {
				fmt.Println("Client is not connected to a node.")
				continue
			}
			activeClient = client
			fmt.Printf("Logged in as client '%s'.\n", activeClient.ClientId)

			// Show connected clients to the same node
			fmt.Printf("Node '%s' has the following clients:\n", client.TargetNode.ID)
			clients := client.TargetNode.GetClientIDs()
			for _, cid := range clients {
				if cid == client.ClientId {
					fmt.Printf("  - %s (you)\n", cid)
				} else {
					fmt.Printf("  - %s\n", cid)
				}
			}

			printClientHelp()

		case "routing":
			if len(args) != 2 {
				fmt.Println("Usage: routing <nodeID>")
				continue
			}
			env.PrintRouting(args[1])
		case "exit":
			env.Shutdown()
			fmt.Println("Shutdown complete.")
			return

		case "quickstart":
			if len(args) != 4 {
				fmt.Println("Usage: quickstart server|client <nodeID> <addr>")
				continue
			}
			role, nodeID, addr := args[1], args[2], args[3]

			var err error
			if role == "server" {
				err = env.AddServer(nodeID, addr, "")
			} else if role == "client" {
				err = env.AddClientNode(nodeID, addr, "")
			} else {
				fmt.Println("Unknown role:", role)
				continue
			}
			if err != nil {
				fmt.Println("Error starting node:", err)
				continue
			}

			clientName := nodeID + "_client"
			err = env.AddClient(clientName)
			if err != nil {
				fmt.Println("Error creating client:", err)
				continue
			}

			node := env.GetNode(nodeID)
			if node == nil {
				fmt.Println("Node was not found after creation.")
				continue
			}

			client := env.GetClient(clientName)
			err = client.Connect(node)
			if err != nil {
				fmt.Println("Error connecting client:", err)
				continue
			}

			activeClient = client
			fmt.Printf("Quickstarted and logged in as '%s'\n", clientName)
			printClientHelp()

		case "nodes":
			if len(env.Nodes) == 0 {
				fmt.Println("No nodes created yet.")
			} else {
				fmt.Println("Existing nodes:")
				for id := range env.Nodes {
					fmt.Println("  -", id)
				}
			}

		case "clients":
			if len(env.Clients) == 0 {
				fmt.Println("No clients created yet.")
			} else {
				fmt.Println("Existing clients:")
				for id := range env.Clients {
					client := env.Clients[id]
					target := "none"
					if client.TargetNode != nil {
						target = client.TargetNode.ID
					}
					fmt.Printf("  - %s (connected to: %s)\n", id, target)
				}
			}

		default:
			fmt.Println("Unknown command. Type 'help' to see available commands.")
		}
	}
}

func printGlobalHelp() {
	fmt.Println("=== Global Commands ===")
	fmt.Println("start server <nodeID> <listenAddr>      - Start a server node")
	fmt.Println("start client <nodeID> <remoteAddr>      - Start a client node and connect it")
	fmt.Println("start server|client <nodeID> <addr> [ws=<port>] - Start a node with optional WebSocket")
	fmt.Println("addclient <clientName>                  - Create a new client")
	fmt.Println("connectclient <clientName> <nodeID>     - Connect a client to a node")
	fmt.Println("login <clientName>                      - Login as a client")
	fmt.Println("routing <nodeID>                        - Print routing table for a node")
	fmt.Println("quickstart server|client <nodeID> <addr> - Start node + client + login automatically")
	fmt.Println("wlogin <clientId> <ws-url>             - Login as WebSocket client to remote node")
	fmt.Println("nodes                                   - List all node IDs")
	fmt.Println("clients                                 - List all client IDs and their connections")
	fmt.Println("exit                                    - Shutdown all and exit")
	fmt.Println("help                                    - Show this help")
}

func printClientHelp() {
	fmt.Println("=== Client Mode Commands ===")
	fmt.Println("sub <topic>                             - Subscribe to a topic")
	fmt.Println("unsub <topic>                           - Unsubscribe from a topic")
	fmt.Println("pub <topic> <message>                   - Publish message to a topic")
	fmt.Println("sayhello <targetNodeID>                 - Call 'whoami' RPC on a node")
	fmt.Println("add <targetNodeID> <a+b>                - Call 'add' RPC to sum two numbers")
	fmt.Println("exit                                    - Exit client session")
	fmt.Println("help                                    - Show this help")
}

func websocketConsoleSession(clientID, wsURL string) {
	printWSClientHelp()
	client := api.NewMajulaClient(wsURL, clientID)

	//client.startHeartbeat(10 * time.Second)
	client.Subscribe("debug", func(topic string, args map[string]interface{}) {
		fmt.Printf("[WS:%s] <- Topic:%s Msg:%v\n", clientID, topic, args)
	})

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("(ws:%s)> ", clientID)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "exit" {
			client.Quit()
			return
		}
		if line == "help" {
			printWSClientHelp()
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			fmt.Println("Usage: pub|sub|unsub|rpc <target> [json]")
			continue
		}
		cmd, arg := parts[0], parts[1]
		data := ""
		if len(parts) == 3 {
			data = parts[2]
		}
		switch cmd {
		case "pub":
			var m map[string]interface{}
			json.Unmarshal([]byte(data), &m)
			client.Publish(arg, m)
		case "sub":
			client.Subscribe(arg, func(topic string, args map[string]interface{}) {
				fmt.Printf("[WS:%s] <- %s: %v\n", clientID, topic, args)
			})
		case "unsub":
			client.Unsubscribe(arg)

		case "rpc":
			newParts := strings.SplitN(data, " ", 3)
			if len(parts) != 3 && (len(newParts) != 2 && len(newParts) != 3) {
				fmt.Println("Usage: rpc <fun> <targetNode> <provider> <json>")
				break
			}
			targetNode := newParts[0]
			provider := newParts[1]
			argsJSON := ""
			if len(newParts) == 3 {
				argsJSON = newParts[2]
			}
			var m map[string]interface{}
			json.Unmarshal([]byte(argsJSON), &m)

			res, ok := client.CallRpc(arg, targetNode, provider, m, 3*time.Second)
			if !ok {
				fmt.Println("RPC call failed or timeout.")
			} else {
				fmt.Println("RPC result:", res)
			}

		case "listrpc":
			if len(parts) != 3 {
				fmt.Println("Usage: listrpc <targetNode> <provider>")
				break
			}

			params := map[string]interface{}{
				"rpcProvider": data,
			}

			res, ok := client.CallRpc("allrpcs", arg, "init", params, 3*time.Second)
			if !ok {
				fmt.Println("Failed to fetch RPC list.")
				break
			}

			switch funcs := res.(type) {
			case []interface{}:
				fmt.Printf("RPCs on node '%s' for provider '%s':\n", arg, data)
				for _, item := range funcs {
					if m, ok := item.(map[string]interface{}); ok {
						name, _ := m["name"].(string)
						note, _ := m["note"].(string)
						fmt.Printf("  - %s: %s\n", name, note)
					}
				}
			default:
				fmt.Println("Unexpected response format:", res)
			}

		case "send":
			if len(parts) != 3 {
				fmt.Println("Usage: send <targetNode> <targetClient> <jsonPayload>")
				break
			}
			subParts := strings.SplitN(data, " ", 2)
			if len(subParts) != 2 {
				fmt.Println("Usage: send <targetNode> <targetClient> <jsonPayload>")
				break
			}
			targetClient := subParts[0]
			payloadRaw := subParts[1]

			var payload map[string]interface{}
			err := json.Unmarshal([]byte(payloadRaw), &payload)
			if err != nil {
				fmt.Println("Invalid JSON:", err)
				break
			}

			client.SendPrivateMessage(arg, targetClient, payload)
			fmt.Printf("Sent private message to %s@%s: %v\n", targetClient, arg, payload)

		default:
			fmt.Println("Unknown command.")
		}
	}
}

func printWSClientHelp() {
	fmt.Println("=== WebSocket Client Mode Commands ===")
	fmt.Println("sub <topic>                      - Subscribe to a topic")
	fmt.Println("unsub <topic>                    - Unsubscribe from a topic")
	fmt.Println("pub <topic> <json>               - Publish JSON to topic")
	fmt.Println("rpc <fun> <targetNode> <provider> <json> - Call remote RPC with JSON args")
	fmt.Println("listrpc <targetNode> <provider>        - List RPCs provided by a node")
	fmt.Println("send <targetNode> <targetClient> <json> - Send private message to a client on a node")
	fmt.Println("help                             - Show this help")
	fmt.Println("exit                             - Quit WebSocket client")
}
