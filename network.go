// network.go — NetworkClient: HTTP với rate-limit mutex và retry/backoff phân loại theo spec 2.6.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	minRequestInterval = 200 * time.Millisecond // rate-limit: tối thiểu giữa 2 request
	httpTimeout        = 10 * time.Second        // timeout mỗi request
	backoffInitial     = 250 * time.Millisecond  // backoff khởi đầu cho 429/5xx/timeout
	backoffCap         = 30 * time.Second        // backoff tối đa
	sleep425           = 500 * time.Millisecond  // 425 = chưa tới giờ, sleep cố định, KHÔNG tính backoff
)

// NetworkClient quản lý toàn bộ HTTP tới server HEXUDON.
// Rate-limit bảo vệ bởi mutex; retry phân loại đúng theo spec 2.6.
type NetworkClient struct {
	apiBase string // base + "/api/v1/matches/" + matchID
	token   string
	mu      sync.Mutex // bảo vệ lastReq
	lastReq time.Time
	client  *http.Client
}

// NewNetworkClient tạo client mới.
func NewNetworkClient(base, matchID, token string) *NetworkClient {
	return &NetworkClient{
		apiBase: base + "/api/v1/matches/" + matchID,
		token:   token,
		client:  &http.Client{Timeout: httpTimeout},
	}
}

// do thực hiện HTTP request, xử lý rate-limit và retry theo spec 2.6:
//   - 200      → parse out, return nil
//   - 425      → sleep cố định (sleep425), retry — KHÔNG log như error, KHÔNG tính backoff
//   - 429      → backoff tăng dần, đọc Retry-After header nếu có
//   - 5xx/net  → backoff tăng dần (exponential, cap backoffCap)
//   - 4xx khác → return error ngay, KHÔNG retry
func (nc *NetworkClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	backoff := backoffInitial
	for {
		// Đảm bảo >= minRequestInterval giữa các lần gọi
		nc.mu.Lock()
		gap := time.Until(nc.lastReq.Add(minRequestInterval))
		nc.mu.Unlock()
		if gap > 0 {
			if !sleepCtx(ctx, gap) {
				return ctx.Err()
			}
		}

		status, retryAfter, netErr := nc.doOnce(ctx, method, path, body, out)

		nc.mu.Lock()
		nc.lastReq = time.Now()
		nc.mu.Unlock()

		switch {
		case netErr != nil:
			log.Printf("[WARN] %s %s: %v (retry in %v)", method, path, netErr, backoff)
			if !sleepCtx(ctx, backoff) {
				return ctx.Err()
			}
			backoff = capDur(backoff*2, backoffCap)

		case status == 200:
			return nil

		case status == 425:
			// Chưa tới giờ — không phải lỗi thật, sleep cố định, không tăng backoff
			if !sleepCtx(ctx, sleep425) {
				return ctx.Err()
			}

		case status == 429:
			delay := backoff
			if retryAfter > 0 {
				delay = retryAfter
			}
			log.Printf("[WARN] %s %s: 429 rate-limited (retry in %v)", method, path, delay)
			if !sleepCtx(ctx, delay) {
				return ctx.Err()
			}
			backoff = capDur(backoff*2, backoffCap)

		case status >= 500:
			log.Printf("[WARN] %s %s: server error %d (retry in %v)", method, path, status, backoff)
			if !sleepCtx(ctx, backoff) {
				return ctx.Err()
			}
			backoff = capDur(backoff*2, backoffCap)

		default:
			// 400/401/403/… — lỗi cứng, raise ngay, không retry
			return fmt.Errorf("HTTP %d: %s %s", status, method, path)
		}
	}
}

// doOnce thực hiện đúng 1 lần HTTP.
// Trả (statusCode, retryAfterDuration, networkError).
func (nc *NetworkClient) doOnce(ctx context.Context, method, path string, body []byte, out any) (int, time.Duration, error) {
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, nc.apiBase+path, rd)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+nc.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := nc.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	// Đọc Retry-After nếu server trả về
	var retryAfter time.Duration
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, e := strconv.Atoi(ra); e == nil && secs > 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
	}

	if resp.StatusCode == 200 && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return 200, 0, fmt.Errorf("decode %s: %w", path, err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, retryAfter, nil
}

// --- API Methods ---

// GetSetup lấy cấu hình trận, retry cho đến khi thành công.
func (nc *NetworkClient) GetSetup(ctx context.Context) (*Setup, error) {
	var s Setup
	if err := nc.do(ctx, http.MethodGet, "/setup", nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// PostAssignment gán loại agent (gửi 1 lần duy nhất, cố định cả trận).
// kinds: mảng phẳng []int như [0,1,0,1] (0=Patrol, 1=Refueler).
func (nc *NetworkClient) PostAssignment(ctx context.Context, kinds []int) error {
	b, err := json.Marshal(kinds)
	if err != nil {
		return err
	}
	return nc.do(ctx, http.MethodPost, "/assignment", b, nil)
}

// WaitStart poll GET /start cho đến khi server sẵn sàng.
// 425 (chưa tới giờ) được xử lý bên trong do() — caller không cần biết.
func (nc *NetworkClient) WaitStart(ctx context.Context) error {
	return nc.do(ctx, http.MethodGet, "/start", nil, nil)
}

// GetState lấy trạng thái đầu ngày.
func (nc *NetworkClient) GetState(ctx context.Context) (*DayState, error) {
	var st DayState
	if err := nc.do(ctx, http.MethodGet, "/state", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// PostActions gửi kế hoạch hành động cho ngày.
// actions: [][]int — mỗi agent 1 dãy hướng(0-5)/WAIT(≤-1).
// Server lấy bản hợp lệ CUỐI CÙNG trong ngày → có thể gửi lại để cải thiện.
func (nc *NetworkClient) PostActions(ctx context.Context, actions [][]int) (*ActionResult, error) {
	b, err := json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	var res ActionResult
	if err := nc.do(ctx, http.MethodPost, "/actions", b, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetResult lấy kết quả cuối trận.
func (nc *NetworkClient) GetResult(ctx context.Context) (json.RawMessage, error) {
	var res json.RawMessage
	if err := nc.do(ctx, http.MethodGet, "/result", nil, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// --- internal helpers ---

// sleepCtx sleep d hoặc thoát sớm nếu ctx bị cancel.
// Trả false nếu ctx done.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// capDur trả min(a, b).
func capDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
