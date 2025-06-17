package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

var env = NewEnv()
var activeClient *Client

func main() {
	reader := bufio.NewReader(os.Stdin)
	printGlobalHelp()

	for {
		if activeClient != nil {
			fmt.Printf("(%s)> ", activeClient.clientId)
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
				fmt.Printf("Exiting client session '%s'\n", activeClient.clientId)
				activeClient = nil
			case "sub":
				if len(args) != 2 {
					fmt.Println("Usage: sub <topic>")
					continue
				}
				topic := args[1]
				err := activeClient.Subscribe(topic, func(topic, from, to string, content []byte) {
					fmt.Printf("[To %s] Topic: %s | From: %s | Message: %s\n", activeClient.clientId, topic, from, string(content))
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
				if activeClient.targetNode == nil {
					fmt.Println("Client is not connected to any node.")
					break
				}
				result, ok := activeClient.targetNode.makeRpcRequest(
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
				result, ok := activeClient.targetNode.makeRpcRequest(
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
		case "help":
			printGlobalHelp()
		case "start":
			if len(args) != 4 {
				fmt.Println("Usage: start server|client <nodeID> <addr>")
				continue
			}
			role, nodeID, addr := args[1], args[2], args[3]
			var err error
			if role == "server" {
				err = env.AddServer(nodeID, addr)
			} else if role == "client" {
				err = env.AddClientNode(nodeID, addr)
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
				fmt.Printf("Client %s connected to node %s\n", client.clientId, node.ID)
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
			if client.targetNode == nil {
				fmt.Println("Client is not connected to a node.")
				continue
			}
			activeClient = client
			fmt.Printf("Logged in as client '%s'.\n", activeClient.clientId)

			// Show connected clients to the same node
			fmt.Printf("Node '%s' has the following clients:\n", client.targetNode.ID)
			clients := client.targetNode.GetClientIDs()
			for _, cid := range clients {
				if cid == client.clientId {
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
				err = env.AddServer(nodeID, addr)
			} else if role == "client" {
				err = env.AddClientNode(nodeID, addr)
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
			if len(env.nodes) == 0 {
				fmt.Println("No nodes created yet.")
			} else {
				fmt.Println("Existing nodes:")
				for id := range env.nodes {
					fmt.Println("  -", id)
				}
			}

		case "clients":
			if len(env.clients) == 0 {
				fmt.Println("No clients created yet.")
			} else {
				fmt.Println("Existing clients:")
				for id := range env.clients {
					client := env.clients[id]
					target := "none"
					if client.targetNode != nil {
						target = client.targetNode.ID
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
	fmt.Println("addclient <clientName>                  - Create a new client")
	fmt.Println("connectclient <clientName> <nodeID>     - Connect a client to a node")
	fmt.Println("login <clientName>                      - Login as a client")
	fmt.Println("routing <nodeID>                        - Print routing table for a node")
	fmt.Println("quickstart server|client <nodeID> <addr> - Start node + client + login automatically")
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
