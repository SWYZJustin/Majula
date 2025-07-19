package core

import "strings"

func parseTopicMessage(data string) (topic string, payload string, ok bool) {
	parts := strings.SplitN(data, "|", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
