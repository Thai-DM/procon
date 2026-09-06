// transport.go — Interface chung cho cả HTTP và WebSocket transport.
// Cho phép main.go chọn transport bằng flag -transport http|ws
// mà không cần thay đổi logic game.
package main

import (
	"context"
	"encoding/json"
)

// GameTransport là interface chung mà cả NetworkClient (HTTP)
// và WsNetworkClient (WebSocket) đều implement.
type GameTransport interface {
	GetSetup(ctx context.Context) (*Setup, error)
	PostAssignment(ctx context.Context, kinds []int) error
	WaitStart(ctx context.Context) error
	GetState(ctx context.Context) (*DayState, error)
	PostActions(ctx context.Context, actions [][]int) (*ActionResult, error)
	GetResult(ctx context.Context) (json.RawMessage, error)
}

// Đảm bảo compile-time check: cả 2 struct implement interface.
var _ GameTransport = (*NetworkClient)(nil)
var _ GameTransport = (*WsNetworkClient)(nil)
