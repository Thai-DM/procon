// ws_network.go — WebSocket transport cho HEXUDON Bot.
// Implement theo đúng spec API:
// URL: ws://.../ws/v1/matches/{id}?token=...
// Message KHÔNG có trường type, phân biệt theo trường có mặt.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// WsNetworkClient kết nối tới server qua WebSocket.
type WsNetworkClient struct {
	conn    *websocket.Conn
	token   string
	matchID string
	baseURL string
	
	// Channels để route tin nhắn nhận được
	setupChan  chan *Setup
	stateChan  chan *DayState
	resultChan chan json.RawMessage
	
	errChan chan error
}

func NewWsNetworkClient(base, matchID, token string) *WsNetworkClient {
	return &WsNetworkClient{
		token:      token,
		matchID:    matchID,
		baseURL:    base,
		setupChan:  make(chan *Setup, 1),
		stateChan:  make(chan *DayState, 100),
		resultChan: make(chan json.RawMessage, 1),
		errChan:    make(chan error, 1),
	}
}

// Connect thiết lập kết nối WebSocket tới server và bắt đầu vòng lặp đọc.
func (wc *WsNetworkClient) Connect(ctx context.Context) error {
	// Base URL: https://procon.ptit.edu.vn -> wss://procon.ptit.edu.vn
	wsBase := strings.Replace(wc.baseURL, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)
	
	// Endpoint theo doc: ws://.../ws/v1/matches/{id}?token=...
	u, err := url.Parse(wsBase + "/ws/v1/matches/" + wc.matchID)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("token", wc.token)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	log.Printf("[WS] Connecting to %s", u.String())
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("WS connect: %w", err)
	}
	wc.conn = conn
	log.Printf("[WS] Connected!")
	
	// Khởi chạy vòng lặp đọc background
	go wc.readLoop(ctx)
	return nil
}

func (wc *WsNetworkClient) Close() {
	if wc.conn != nil {
		wc.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		wc.conn.Close()
	}
}

// readLoop liên tục đọc JSON từ WS và phân loại dựa vào các trường có mặt.
func (wc *WsNetworkClient) readLoop(ctx context.Context) {
	for {
		_, b, err := wc.conn.ReadMessage()
		if err != nil {
			wc.errChan <- fmt.Errorf("WS read error: %w", err)
			return
		}
		
		// Phân biệt message bằng cách unmarshal vào map
		var raw map[string]interface{}
		if err := json.Unmarshal(b, &raw); err != nil {
			log.Printf("[WS] Unparseable message: %s", string(b))
			continue
		}
		
		switch {
		case raw["map"] != nil || raw["daySteps"] != nil:
			// Setup
			var setup Setup
			if err := json.Unmarshal(b, &setup); err == nil {
				wc.setupChan <- &setup
			}
		case raw["day"] != nil && raw["traffics"] != nil:
			// DayState
			var st DayState
			if err := json.Unmarshal(b, &st); err == nil {
				wc.stateChan <- &st
			}
		case raw["standings"] != nil:
			// MatchResult
			wc.resultChan <- json.RawMessage(b)
		case raw["ok"] != nil || raw["reason"] != nil:
			// ActionResult (nếu server có gửi)
			log.Printf("[WS] Server sent ActionResult: %s", string(b))
		default:
			log.Printf("[WS] Unknown message format: %s", string(b))
		}
	}
}

func (wc *WsNetworkClient) send(data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return wc.conn.WriteMessage(websocket.TextMessage, b)
}

// GetSetup chờ nhận Setup từ server (server đẩy đầu tiên).
func (wc *WsNetworkClient) GetSetup(ctx context.Context) (*Setup, error) {
	select {
	case setup := <-wc.setupChan:
		return setup, nil
	case err := <-wc.errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PostAssignment gửi loại agent qua WS đồng thời gửi fallback qua HTTP REST
// để tương thích hoàn toàn cả với WS-native server (PTIT) và REST-only server.
func (wc *WsNetworkClient) PostAssignment(ctx context.Context, kinds []int) error {
	wsErr := wc.send(kinds)

	// Gửi thêm HTTP POST /api/v1/matches/{id}/assignment fallback
	b, err := json.Marshal(kinds)
	if err == nil {
		apiURL := fmt.Sprintf("%s/api/v1/matches/%s/assignment", wc.baseURL, wc.matchID)
		req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(b))
		if errReq == nil {
			req.Header.Set("Authorization", "Bearer "+wc.token)
			req.Header.Set("Content-Type", "application/json")
			httpClient := &http.Client{Timeout: 3 * time.Second}
			resp, errDo := httpClient.Do(req)
			if errDo == nil {
				defer resp.Body.Close()
				log.Printf("[INIT] HTTP fallback PostAssignment status: %d", resp.StatusCode)
			}
		}
	}

	return wsErr
}

// WaitStart chờ bắt đầu. Với WS, trận bắt đầu khi ta nhận được DayState ngày 0.
// Do đó hàm này chỉ return nil, vòng lặp game sẽ bị block ở GetState(0).
func (wc *WsNetworkClient) WaitStart(ctx context.Context) error {
	return nil
}

// GetState chờ nhận state ngày mới.
func (wc *WsNetworkClient) GetState(ctx context.Context) (*DayState, error) {
	select {
	case st := <-wc.stateChan:
		// Drain mọi state cũ bị dồn hàng đợi (nếu có lag mạng) để luôn xử lý ngày mới nhất của server
		for {
			select {
			case newer := <-wc.stateChan:
				log.Printf("[WS] ⏩ Bỏ qua state cũ Day %d, chuyển sang Day %d mới nhất", st.Day, newer.Day)
				st = newer
			default:
				return st, nil
			}
		}
	case err := <-wc.errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PostActions gửi kế hoạch hành động. (Client chỉ gửi array of arrays)
func (wc *WsNetworkClient) PostActions(ctx context.Context, actions [][]int) (*ActionResult, error) {
	err := wc.send(actions)
	if err != nil {
		return nil, err
	}
	// Do server WS có thể không gửi ACK cho actions, ta return OK ảo.
	// Nếu action sai, server sẽ ngầm hủy. (Theo tài liệu BTC không thấy nhắc tới error response cho actions trong WS).
	return &ActionResult{Ok: true}, nil
}

// GetResult nhận kết quả cuối trận.
func (wc *WsNetworkClient) GetResult(ctx context.Context) (json.RawMessage, error) {
	select {
	case res := <-wc.resultChan:
		return res, nil
	case err := <-wc.errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
