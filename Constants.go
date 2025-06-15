package main

import "time"

// background线程频率相关
const (
	CostCheckTimePeriod          time.Duration = 1 * time.Second
	BuildUpTimePeriod            time.Duration = 500 * time.Millisecond
	HeartBeatTimePeriod          time.Duration = 1000 * time.Millisecond
	ReceivedMessageCleanUpPeriod time.Duration = 10 * time.Second
	RetryLoopPeriod              time.Duration = 1 * time.Second
	SubscribeFloodTicket         time.Duration = 1 * time.Second
	DebugPrint                   bool          = false
	DefaultRpcOvertime           time.Duration = 6 * time.Second
	RpcFloodTicket               time.Duration = 10 * time.Second
	RpcCacheCleanTicket          time.Duration = 1 * time.Minute
)
