package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBuildRoutingTable(t *testing.T) {
	node := &Node{
		ID: "A",
		LinkSet: LinkSetType{
			"A": {
				"B": {Source: "A", Target: "B", Cost: 1, Channel: "ch1"},
				"C": {Source: "A", Target: "C", Cost: 2, Channel: "ch2"},
			},
			"B": {
				"C": {Source: "B", Target: "C", Cost: 2, Channel: "ch3"},
			},
			"C": {},
			"D": {},
		},
		RoutingTable: make(RoutingTableType),
		LinkSetMutex: sync.RWMutex{},
	}
	node.buildRoutingTable()
	fmt.Println("Routing Table for Node A:")
	for target, routes := range node.RoutingTable {
		fmt.Printf("Target: %s, Next Hop: %s, Channel: %s\n", target, routes[0].nextHopNodeID, routes[0].LocalChannelID)
	}
}

func TestRunEnv(t *testing.T) {
	nodes := []string{"A", "B", "C"}

	pairs := []SimplePair{
		newSimplePair([]string{"A", "B"}),
		newSimplePair([]string{"B", "C"}),
	}

	env := NewSimpleEnvEx(nodes, pairs)
	env.show()

	fmt.Println("Starting the environment...")
	env.runOld()
	fmt.Println("Environment stopped.")
}

func TestRunEnv_Complex(t *testing.T) {
	nodes := []string{"A", "B", "C", "D", "E", "F"}

	// More connections, including cycles and redundant paths
	pairs := []SimplePair{
		newSimplePair([]string{"A", "B"}),
		newSimplePair([]string{"A", "C"}),
		newSimplePair([]string{"B", "C"}),
		newSimplePair([]string{"C", "D"}),
		newSimplePair([]string{"D", "E"}),
		newSimplePair([]string{"E", "F"}),
		newSimplePair([]string{"F", "A"}), // cycle back to A
		newSimplePair([]string{"B", "E"}), // shortcut path
	}

	env := NewSimpleEnvEx(nodes, pairs)
	env.show()

	fmt.Println("Starting the complex environment...")
	env.runOld()
	fmt.Println("Environment stopped.")
}

type DummyNode struct {
	LinkSet      LinkSetType
	LinkSetMutex sync.RWMutex
}

func (node *DummyNode) serializeLinkSet() string {
	node.LinkSetMutex.RLock()
	defer node.LinkSetMutex.RUnlock()
	filteredLinkSet := make(LinkSetType)

	for key1, innerMap := range node.LinkSet {
		filteredLinkSet[key1] = make(map[string]Link)
		for key2, link := range innerMap {
			if link.Cost != -1 {
				filteredLinkSet[key1][key2] = link
			}
		}
	}
	data, err := json.Marshal(filteredLinkSet)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestLinkSetSerialization(t *testing.T) {
	linkSet := LinkSetType{
		"A": {
			"B": Link{
				Source:  "A",
				Target:  "B",
				Cost:    123,
				Version: 1,
				Channel: "ch1",
			},
		},
	}

	node := DummyNode{
		LinkSet: linkSet,
	}

	serialized := node.serializeLinkSet()
	t.Logf("Serialized: %s", serialized)

	deserialized := deserializeLinkSet(serialized)
	if deserialized == nil {
		t.Errorf("Deserialization returned nil")
	} else {
		t.Logf("Deserialized: %+v", deserialized)
	}
}

/*
    C1       C2
     |        |
    S1       S2
     \      /
       C3 (bridge)
     /      \
   S3        S4
    |        |
   C4       C5

*/

func TestRunBridgeTopologyEnv(t *testing.T) {
	env := NewTcpEnv(25555, "bridge-token")
	env.addSimpleServer("S1")
	env.addSimpleServer("S2")
	env.addSimpleServer("S3")
	env.addSimpleServer("S4")
	env.addSimpleClient("C1", "S1")
	env.addSimpleClient("C2", "S2")
	env.addSimpleClient("C4", "S3")
	env.addSimpleClient("C5", "S4")
	env.addSimpleClient("C3", "S1")
	env.connectClientToServer("C3", "S2")
	env.connectClientToServer("C3", "S3")
	env.connectClientToServer("C3", "S4")
	env.startAll()

	time.Sleep(10 * time.Second)
	env.end()
	time.Sleep(2 * time.Second)
	env.printAllRoutingTables()
}

func TestRunTcpEnv(t *testing.T) {
	env := NewTcpEnv(22223, "test-token")

	// Setup TCP nodes
	env.addSimpleServer("S1")
	env.addSimpleClient("C1", "S1")
	env.addSimpleClient("C2", "S1")

	env.startAll()
	time.Sleep(10 * time.Second)
	env.end()
	time.Sleep(2 * time.Second)
	env.printAllRoutingTables()

}
