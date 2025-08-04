package core

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Link 表示节点间的连接链路，包含源节点、目标节点、代价、版本等信息
type Link struct {
	Source         string `json:"source"`
	Target         string `json:"target"`
	Cost           int64  `json:"cost"`
	Version        int64  `json:"version"`
	Channel        string // Channel是提供给本地的channel索引
	LastUpdateTime time.Time
}

// costEqual 比较两个代价是否相等
func costEqual(cost1 int64, cost2 int64) bool {
	return cost1 == cost2
}

// setCost 设置链路的预估代价
func (link *Link) setCost(pCost int64) {
	link.Cost = pCost
}

// addVersion 将链路版本号加1
func (link *Link) addVersion() {
	atomic.AddInt64(&link.Version, 1)
}

// updateVersion 更新链路版本号
func (link *Link) updateVersion(newVersion int64) {
	atomic.StoreInt64(&link.Version, newVersion)
}

// setLastUpdateTime 设置最后更新时间
func (link *Link) setLastUpdateTime() {
	link.LastUpdateTime = time.Now()
}

// checkOutOfTime 检查链路是否超时（超过10秒未更新）
func (link *Link) checkOutOfTime() bool {
	now := time.Now()
	timeSinceLastUpdate := now.Sub(link.LastUpdateTime)
	return timeSinceLastUpdate > 10*time.Second
}

// printLink 打印链路信息到控制台
func (link *Link) printLink() {
	fmt.Printf("Link:\n")
	fmt.Printf("  Source:         %s\n", link.Source)
	fmt.Printf("  Target:         %s\n", link.Target)
	fmt.Printf("  Cost:           %d\n", link.Cost)
	fmt.Printf("  Version:        %d\n", atomic.LoadInt64(&link.Version))
	fmt.Printf("  Channel:        %s\n", link.Channel)
	fmt.Printf("  LastUpdateTime: %s\n", link.LastUpdateTime.Format(time.RFC3339))
}

// printLinkS 返回链路信息的字符串表示
func (link *Link) printLinkS() string {
	var builder strings.Builder
	builder.WriteString("Link:\n")
	builder.WriteString(fmt.Sprintf("  Source:         %s\n", link.Source))
	builder.WriteString(fmt.Sprintf("  Target:         %s\n", link.Target))
	builder.WriteString(fmt.Sprintf("  Cost:           %d\n", link.Cost))
	builder.WriteString(fmt.Sprintf("  Version:        %d\n", atomic.LoadInt64(&link.Version)))
	builder.WriteString(fmt.Sprintf("  Channel:        %s\n", link.Channel))
	builder.WriteString(fmt.Sprintf("  LastUpdateTime: %s\n", link.LastUpdateTime.Format(time.RFC3339)))
	return builder.String()
}
