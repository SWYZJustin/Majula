package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// =====================
// SimpleChannelWorker
// =====================

type SimpleChannelWorker struct {
	ID               string
	ChannelUsers     map[string]ChannelUser // String is the channel's node id
	SimulateWaitTime int
	myMutex          sync.Mutex
}

func (CWorker *SimpleChannelWorker) addChannelUser(pNodeId string, channelUser ChannelUser) {
	if CWorker.ChannelUsers == nil {
		CWorker.ChannelUsers = make(map[string]ChannelUser)
	}
	CWorker.ChannelUsers[pNodeId] = channelUser
}

func newSimpleChannelWorker() *SimpleChannelWorker {
	return &SimpleChannelWorker{
		ChannelUsers:     make(map[string]ChannelUser),
		SimulateWaitTime: rand.Intn(10) + 1,
		myMutex:          sync.Mutex{},
	}
}

func NewSimpleChannelWorkerFull(pId string, channelUsers map[string]ChannelUser) *SimpleChannelWorker {
	CWorker := SimpleChannelWorker{
		ID:               pId,
		ChannelUsers:     channelUsers,
		SimulateWaitTime: rand.Intn(10) + 1,
		myMutex:          sync.Mutex{},
	}

	return &CWorker
}

func (CWorker *SimpleChannelWorker) getID() string {
	return CWorker.ID
}

func (CWorker *SimpleChannelWorker) sendTo(nextHopNodeId string, msg *Message) {
	CWorker.myMutex.Lock()
	defer CWorker.myMutex.Unlock()
	target, ok := CWorker.ChannelUsers[nextHopNodeId]
	if !ok {
		//panic(fmt.Sprintf("Target Channel Not Found: nextHopNodeId=%s, message=%+v", nextHopNodeId, msg.Print()))
		return
	}
	go func() {
		waitTime := time.Duration(CWorker.SimulateWaitTime) * time.Millisecond
		time.Sleep(waitTime)
		target.onRecv(msg.From, msg)
	}()
}

func (CWorker *SimpleChannelWorker) broadCast(msg *Message) {
	CWorker.myMutex.Lock()
	defer CWorker.myMutex.Unlock()

	go func() {
		waitTime := time.Duration(CWorker.SimulateWaitTime) * time.Millisecond
		time.Sleep(waitTime)
		for _, user := range CWorker.ChannelUsers {
			user.onRecv(msg.From, msg)
		}
	}()
}

func (CWorker *SimpleChannelWorker) print() {
	CWorker.myMutex.Lock()
	defer CWorker.myMutex.Unlock()
	for nodeID := range CWorker.ChannelUsers {
		fmt.Printf("NodeID: %s ", nodeID)
	}
	fmt.Printf("\n")
	fmt.Printf("SimulateWaitTime: %d\n", CWorker.SimulateWaitTime)
}

func (CWorker *SimpleChannelWorker) Close() error {
	return nil
}
