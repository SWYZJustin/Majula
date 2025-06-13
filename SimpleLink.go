package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

var appRegistry = map[string]*ClientApp{}

func main() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Distributed Node CLI")
	fmt.Println("Commands:")
	fmt.Println("  start server <nodeID> <listenAddr>")
	fmt.Println("  start client <nodeID> <remoteAddr>")
	fmt.Println("  send <fromID> <toID> <message>")
	fmt.Println("  routing <nodeID>")
	fmt.Println("  sub <nodeID> <topic> <clientName>")
	fmt.Println("  pub <nodeID> <topic> <message>")
	fmt.Println("  totalsub <nodeID>")
	fmt.Println("  quit <nodeID>")
	fmt.Println("  exit")

	for {
		fmt.Print(">> ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		args := strings.Split(line, " ")
		switch args[0] {
		case "start":
			if len(args) != 4 {
				fmt.Println("Usage: start server|client <nodeID> <addr>")
				continue
			}
			nodeType, nodeID, addr := args[1], args[2], args[3]
			if _, exists := appRegistry[nodeID]; exists {
				fmt.Println("Node already exists:", nodeID)
				continue
			}
			if nodeType == "server" {
				appRegistry[nodeID] = NewSimpleServer(nodeID, addr)
			} else if nodeType == "client" {
				appRegistry[nodeID] = NewSimpleClient(nodeID, addr)
			} else {
				fmt.Println("Unknown node type:", nodeType)
			}
		case "send":
			if len(args) < 4 {
				fmt.Println("Usage: send <fromID> <toID> <message>")
				continue
			}
			fromID, toID := args[1], args[2]
			message := strings.Join(args[3:], " ")
			if app, ok := appRegistry[fromID]; ok {
				app.Send(toID, message)
			} else {
				fmt.Println("Sender node not found:", fromID)
			}
		case "routing":
			if len(args) != 2 {
				fmt.Println("Usage: routing <nodeID>")
				continue
			}
			nodeID := args[1]
			if app, ok := appRegistry[nodeID]; ok {
				app.PrintRouting()
			} else {
				fmt.Println("Node not found:", nodeID)
			}
		case "quit":
			if len(args) != 2 {
				fmt.Println("Usage: quit <nodeID>")
				continue
			}
			nodeID := args[1]
			if app, ok := appRegistry[nodeID]; ok {
				app.Shutdown()
				delete(appRegistry, nodeID)
			} else {
				fmt.Println("Node not found:", nodeID)
			}
		case "exit":
			for _, app := range appRegistry {
				app.Shutdown()
			}
			fmt.Println("Exiting.")
			return

		case "sub":
			if len(args) != 4 {
				fmt.Println("Usage: sub <nodeID> <topic> <clientName>")
				continue
			}
			nodeID, topic, clientName := args[1], args[2], args[3]
			if app, ok := appRegistry[nodeID]; ok {
				app.Node.addLocalSub(topic, clientName, func(topic, from, to string, content []byte) {
					fmt.Printf("[Callback:%s] Topic: %s | From: %s | Msg: %s\n", clientName, topic, from, string(content))
				})
				fmt.Println("Subscribed to topic:", topic)
			} else {
				fmt.Println("Node not found:", nodeID)
			}

		case "pub":
			if len(args) < 4 {
				fmt.Println("Usage: pub <nodeID> <topic> <message>")
				continue
			}
			nodeID, topic := args[1], args[2]
			message := strings.Join(args[3:], " ")
			if app, ok := appRegistry[nodeID]; ok {
				app.Node.publishOnTopic(topic, message)
				fmt.Println("Published message to topic:", topic)
			} else {
				fmt.Println("Node not found:", nodeID)
			}
		case "totalsub":
			if len(args) != 2 {
				fmt.Println("Usage: totalsub <nodeID>")
				continue
			}
			nodeID := args[1]
			if app, ok := appRegistry[nodeID]; ok {
				app.Node.PrintTotalSubs()
			} else {
				fmt.Println("Node not found:", nodeID)
			}
		default:
			fmt.Println("Unknown command. Type `exit` to quit.")
		}

		time.Sleep(200 * time.Millisecond)
	}
}
