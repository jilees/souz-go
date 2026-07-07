package mcp

import "encoding/json"

// rpcMessage is a superset envelope covering JSON-RPC 2.0 requests,
// responses, and notifications, so a single Unmarshal can sniff which kind
// an incoming message is (see Client.pump).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return e.Message
}

// isResponse reports whether this message is a response to a call we made
// (has an id and either a result or an error, but no method).
func (m rpcMessage) isResponse() bool {
	return m.Method == "" && len(m.ID) > 0 && (m.Result != nil || m.Error != nil)
}

func newRequest(id int64, method string, params any) (json.RawMessage, error) {
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: paramsJSON})
}

func newNotification(method string, params any) (json.RawMessage, error) {
	paramsJSON, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: paramsJSON})
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}
