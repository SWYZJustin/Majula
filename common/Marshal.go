package common

import (
	"encoding/json"
	"github.com/vmihailenco/msgpack/v5"
)

var UseMsgPack bool = false

func MarshalJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func UnmarshalJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func MarshalMsgPack(v interface{}) ([]byte, error) {
	return msgpack.Marshal(v)
}

func UnmarshalMsgPack(data []byte, v interface{}) error {
	return msgpack.Unmarshal(data, v)
}

func MarshalAny(v interface{}) ([]byte, error) {
	if UseMsgPack {

		return MarshalMsgPack(v)
	}
	return MarshalJSON(v)
}

func UnmarshalAny(data []byte, v interface{}) error {
	if UseMsgPack {
		return UnmarshalMsgPack(data, v)
	}
	return UnmarshalJSON(data, v)
}
