package core

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Item represents an item in the priority queue
type Item struct {
	NodeID string
	Cost   int64
	Index  int
}

type PriorityQueue []*Item

func (pq *PriorityQueue) Len() int {
	return len(*pq)
}

func (pq *PriorityQueue) Less(i, j int) bool {
	return (*pq)[i].Cost < (*pq)[j].Cost
}

func (pq *PriorityQueue) Swap(i, j int) {
	(*pq)[i], (*pq)[j] = (*pq)[j], (*pq)[i]
	(*pq)[i].Index = i
	(*pq)[j].Index = j
}

func (pq *PriorityQueue) Push(x interface{}) {
	item := x.(*Item)
	item.Index = len(*pq)
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.Index = -1
	*pq = old[0 : n-1]
	return item
}

func SplitToNodeAndEdges(ft string) ([]string, []int, bool) {
	n := len(ft)
	nodes := []string{}
	edges := []int{}
	j := 0
	for i := 0; i < n; {
		c := ft[i]
		if (c == '<' && i < n-2 && ft[i+1] == '-') || c == '-' && i < n-2 && ft[i+1] == '>' {
			if j >= i {
				return nil, nil, false
			}
			s := strings.Trim(ft[j:i], "\t \n")
			if len(s) == 0 {
				return nil, nil, false
			}
			i += 2
			j = i
			nodes = append(nodes, s)
			if c == '<' {
				edges = append(edges, -1)
			} else {
				edges = append(edges, 1)
			}
		} else if c == '-' {
			if j >= i {
				return nil, nil, false
			}
			s := strings.Trim(ft[j:i], "\t \n")
			if len(s) == 0 {
				return nil, nil, false
			}
			i++
			j = i
			nodes = append(nodes, s)
			edges = append(edges, 0)
		} else {
			i++
		}
	}
	if j >= n {
		return nil, nil, false
	}
	s := strings.Trim(ft[j:n], "\t \n")
	if len(s) == 0 {
		return nil, nil, false
	}
	nodes = append(nodes, s)

	if len(nodes) != len(edges)+1 {
		return nil, nil, false
	}
	return nodes, edges, true
}

func HashIDWithToken(id string, token string) string {
	data := id + token

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func VerifyHash(id string, token string, storedHash string) bool {
	newHash := HashIDWithToken(id, token)
	return newHash == storedHash
}

// HMACMD5Sign 生成HMAC-MD5签名，返回hex字符串
func HMACMD5Sign(data, token string) string {
	mac := hmac.New(md5.New, []byte(token))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACMD5Verify 验证HMAC-MD5签名
func HMACMD5Verify(data, token, sig string) bool {
	expected := HMACMD5Sign(data, token)
	return hmac.Equal([]byte(expected), []byte(sig))
}

func getString(args map[string]interface{}, key, def string) string {
	if v, ok := args[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
		return fmt.Sprintf("%v", v)
	}
	return def
}
