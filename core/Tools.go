package core

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

const (
	LogLevelDetail = iota
	LogLevelDebug
	LogLevelNormal
	LogLevelWarning
	LogLevelError
	LogLevelFatal
	LogLevelMark
)

var (
	LogLevel int = LogLevelMark
)

// 将日志等级转成字符串
func getLogLevelString(logLevel int) string {
	if logLevel >= LogLevelMark {
		return "MARK"
	} else if logLevel >= LogLevelFatal {
		return "FATAL"
	} else if logLevel >= LogLevelError {
		return "ERROR"
	} else if logLevel >= LogLevelWarning {
		return "WARNING"
	} else if logLevel >= LogLevelNormal {
		return "NORMAL"
	} else if logLevel >= LogLevelDebug {
		return "DEBUG"
	} else {
		return "DETAIL"
	}
}

// 根据字符串设置日志等级
func SetLogLevel(slogLevel string) {
	for k, v := range map[string]int{
		"detail":  LogLevelDetail,
		"debug":   LogLevelDebug,
		"normal":  LogLevelNormal,
		"warning": LogLevelWarning,
		"error":   LogLevelError,
		"fatal":   LogLevelFatal,
		"mark":    LogLevelMark,
	} {
		if strings.Contains(strings.ToLower(slogLevel), k) {
			LogLevel = v
			break
		}
	}
}

// 格式化日志消息
func formatLog(msg ...interface{}) string {
	if len(msg) == 0 {
		return ""
	}
	ret := make([]string, len(msg))
	for i, m := range msg {
		ret[i] = fmt.Sprintf("%+v", m)
	}
	return strings.Join(ret, " ")
}

// 核心日志输出函数
func logOnLevel_(level int, callDeep int, msg ...interface{}) bool {
	if level >= LogLevel {
		funcName, file, line, _ := runtime.Caller(callDeep)
		_, short := filepath.Split(file)
		file = short
		out := os.Stdout
		if level >= LogLevelError {
			out = os.Stderr
		}
		if len(msg) > 0 {
			fmt.Fprintf(out, "%s %s %s:%d %s %s\n",
				getLogLevelString(level),
				time.Now().Format("15:04:05.000000"),
				file, line,
				runtime.FuncForPC(funcName).Name(),
				formatLog(msg...))
		} else {
			fmt.Fprintf(out, "%s %s %s:%d %s\n",
				getLogLevelString(level),
				time.Now().Format("15:04:05.000000"),
				file, line,
				runtime.FuncForPC(funcName).Name())
		}
		return true
	}
	return false
}

// 常用日志函数
func Log(msg ...interface{}) {
	logOnLevel_(LogLevelNormal, 2, msg...)
}

func Debug(msg ...interface{}) {
	logOnLevel_(LogLevelDebug, 2, msg...)
}

func Warning(msg ...interface{}) {
	logOnLevel_(LogLevelWarning, 2, msg...)
}

func Error(msg ...interface{}) {
	logOnLevel_(LogLevelError, 2, msg...)
}

func MarkIt(msg ...interface{}) {
	logOnLevel_(LogLevelMark, 2, msg...)
}

func LogOnLevel(level int, msg ...interface{}) bool {
	return logOnLevel_(level, 2, msg...)
}

func LogIf(cond bool, msg ...interface{}) bool {
	if cond {
		logOnLevel_(LogLevelNormal, 2, msg...)
	}
	return cond
}

func LogOnError(err error, msg ...interface{}) error {
	if err != nil {
		msg = append([]interface{}{err.Error()}, msg...)
		logOnLevel_(LogLevelError, 2, msg...)
	}
	return err
}

func PanicOnError(err error, msg ...interface{}) {
	if err != nil {
		msg = append([]interface{}{fmt.Sprintf("Exit On Error : %s", err.Error())}, msg...)
		logOnLevel_(LogLevelError, 2, msg...)
		panic(err)
	}
}
