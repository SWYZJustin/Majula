package common

import "time"

// ===================== 可通过配置文件修改的参数 =====================
var (
	// 这些参数建议暴露给用户配置
	CostCheckTimePeriod          = 1 * time.Second
	BuildUpTimePeriod            = 500 * time.Millisecond
	HeartBeatTimePeriod          = 1000 * time.Millisecond
	ReceivedMessageCleanUpPeriod = 10 * time.Second
	RetryLoopPeriod              = 1 * time.Second
	SubscribeFloodTicket         = 1 * time.Second
	DefaultRpcOvertime           = 6 * time.Second
	RpcFloodTicket               = 10 * time.Second
	RpcCacheCleanTicket          = 1 * time.Minute
	DefaultMessageTTL            = int16(100) // 建议可配置
)

// ===================== 内部固定参数（无需用户配置） =====================
var (
	DebugPrint = false

	// FRP/流控/滑窗相关
	MaxSendWindowSize = 7024
	MaxRetryCount     = 5
	AckTimeout        = 5 * time.Second
	RecvAckThreshold  = 128
	RecvAckTimeout    = 200 * time.Millisecond
	SendResendTimeout = 200 * time.Millisecond
	MaxResendPerCall  = 100

	// TCP注册相关
	MaxRegistrationTime = 10

	// 通道队列长度
	ChannelQueueSizeSmall = 64
	ChannelQueueSizeLarge = 1024
)
