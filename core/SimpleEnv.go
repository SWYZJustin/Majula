package core

import (
	"fmt"
	"time"
)

type SimpleEnv struct {
	Nodes   map[string]*Node
	Workers []*SimpleChannelWorker
}

type SimplePair struct {
	connectedNodes []string
}

func newSimplePair(nodes []string) SimplePair {
	newPair := SimplePair{connectedNodes: nodes}
	return newPair
}

func NewSimpleEnvEx(pNodes []string, pPairs []SimplePair) *SimpleEnv {
	aNode := make(map[string]*Node)
	aWorker := make([]*SimpleChannelWorker, 0)
	for _, pName := range pNodes {
		if _, ok := aNode[pName]; !ok {
			aNode[pName] = NewNode(pName)
		}
	}
	for _, pPair := range pPairs {
		tChannelWorker := newSimpleChannelWorker()
		aWorker = append(aWorker, tChannelWorker)
		for _, connectedNode := range pPair.connectedNodes {
			println(connectedNode, pPair.connectedNodes)
			tChannel := NewChannel()
			tChannel.setChannelWorker(tChannelWorker)
			tChannelWorker.addChannelUser(connectedNode, tChannel)
			aNode[connectedNode].addChannel(tChannel)
			for _, n := range pPair.connectedNodes {
				if n != connectedNode {
					tChannel.addChannelPeer(n)
				}
			}
		}
	}
	newEnv := &SimpleEnv{Nodes: aNode, Workers: aWorker}
	return newEnv
}

func (env *SimpleEnv) start() {
	for _, node := range env.Nodes {
		go node.register()
	}
}

func (env *SimpleEnv) quit() {
	for _, node := range env.Nodes {
		go node.quit()
	}
}

func (env *SimpleEnv) run() {
	for _, worker := range env.Workers {
		worker.print()
	}
	env.start()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Second)

	for {
		select {
		case <-ticker.C:
			fmt.Println("=== Routing Tables ===")
			for _, node := range env.Nodes {
				node.printRoutingTable()
				fmt.Println()
			}
		case <-timeout:
			env.quit()
			return
		}
	}
}

func (env *SimpleEnv) runOld() {
	for _, worker := range env.Workers {
		worker.print()
	}
	env.start()
	time.Sleep(10 * time.Second)
	env.quit()
	time.Sleep(2 * time.Second)
	for _, node := range env.Nodes {
		node.printRoutingTable()
		fmt.Println()
	}
}

func (env *SimpleEnv) show() {
	for _, node := range env.Nodes {
		node.printAllChannels()
	}
}
