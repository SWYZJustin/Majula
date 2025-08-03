package common

import (
	"encoding/json"
	"github.com/vmihailenco/msgpack/v5"
)

// UseMsgPack controls whether to use MessagePack (true) or JSON (false) for serialization.
var UseMsgPack bool = false

// MarshalJSON serializes data to JSON.
func MarshalJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// UnmarshalJSON deserializes JSON data.
func UnmarshalJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// MarshalMsgPack serializes data to MessagePack.
func MarshalMsgPack(v interface{}) ([]byte, error) {
	return msgpack.Marshal(v)
}

// UnmarshalMsgPack deserializes MessagePack data.
func UnmarshalMsgPack(data []byte, v interface{}) error {
	return msgpack.Unmarshal(data, v)
}

// MarshalAny serializes data using the selected protocol.
func MarshalAny(v interface{}) ([]byte, error) {
	if UseMsgPack {

		return MarshalMsgPack(v)
	}
	return MarshalJSON(v)
}

// UnmarshalAny deserializes data using the selected protocol.
func UnmarshalAny(data []byte, v interface{}) error {
	if UseMsgPack {
		return UnmarshalMsgPack(data, v)
	}
	return UnmarshalJSON(data, v)
}
