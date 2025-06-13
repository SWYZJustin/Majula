package main

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

type Link struct { // Link类型相关
	Source         string `json:"source"`
	Target         string `json:"target"`
	Cost           int64  `json:"cost"`
	Version        int64  `json:"version"`
	Channel        string // Channel是提供给本地的channel索引
	LastUpdateTime time.Time
}

func costEqual(cost1 int64, cost2 int64) bool {
	return cost1 == cost2
}

// 设置link的预估代价
func (link *Link) setCost(pCost int64) {
	link.Cost = pCost
}

// 将link的版本+1
func (link *Link) addVersion() {
	atomic.AddInt64(&link.Version, 1)
}

func (link *Link) updateVersion(newVersion int64) {
	atomic.StoreInt64(&link.Version, newVersion)
}

func (link *Link) setLastUpdateTime() {
	link.LastUpdateTime = time.Now()
}

func (link *Link) checkOutOfTime() bool {
	now := time.Now()
	timeSinceLastUpdate := now.Sub(link.LastUpdateTime)
	return timeSinceLastUpdate > 10*time.Second
}

func (link *Link) printLink() {
	fmt.Printf("Link:\n")
	fmt.Printf("  Source:         %s\n", link.Source)
	fmt.Printf("  Target:         %s\n", link.Target)
	fmt.Printf("  Cost:           %d\n", link.Cost)
	fmt.Printf("  Version:        %d\n", atomic.LoadInt64(&link.Version))
	fmt.Printf("  Channel:        %s\n", link.Channel)
	fmt.Printf("  LastUpdateTime: %s\n", link.LastUpdateTime.Format(time.RFC3339))
}

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
