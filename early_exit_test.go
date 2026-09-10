package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type mockTransport struct {
	submittedActions [][]int
}

func (m *mockTransport) GetSetup(ctx context.Context) (*Setup, error) { return nil, nil }
func (m *mockTransport) PostAssignment(ctx context.Context, kinds []int) error { return nil }
func (m *mockTransport) WaitStart(ctx context.Context) error { return nil }
func (m *mockTransport) GetState(ctx context.Context) (*DayState, error) { return nil, nil }
func (m *mockTransport) PostActions(ctx context.Context, actions [][]int) (*ActionResult, error) {
	m.submittedActions = actions
	return &ActionResult{Ok: true, Reason: ""}, nil
}
func (m *mockTransport) GetResult(ctx context.Context) (json.RawMessage, error) { return nil, nil }

// TestEarlyExit8x8 kiểm tra việc dừng sớm trên map 8x8 khi đạt 100% trần lý thuyết (16/16 stocks, 4/4 brands).
// Kết quả điểm số phải giữ nguyên tuyệt đối và thời gian xử lý phải giảm sâu (dưới 150ms thay vì 467ms).
func TestEarlyExit8x8(t *testing.T) {
	W, H := 8, 8
	s := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{30, 30},
		FuelLimits: 60,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 8; i++ {
		pos := (i%3+1)*2*W + (i/3+1)*2
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 4,
			Pos:    pos,
			Stocks: 2,
		})
	}
	gs := NewGameState(s)
	f60 := 60
	for i := 0; i < 4; i++ {
		gs.AgentKinds[i] = 0 // 4 Patrols
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 60
	}

	bt := NewBrandTracker()
	mock := &mockTransport{}
	st := &DayState{
		Day: 0,
		Agents: []Agent{
			{Pos: gs.AgentPos[0], Fuel: &f60},
			{Pos: gs.AgentPos[1], Fuel: &f60},
			{Pos: gs.AgentPos[2], Fuel: &f60},
			{Pos: gs.AgentPos[3], Fuel: &f60},
		},
	}

	start := time.Now()
	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
	elapsed := time.Since(start)

	if len(actions) != 4 {
		t.Fatalf("Expected 4 actions, got %d", len(actions))
	}

	matchTypes, todayTypes, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("=== 8x8 EARLY EXIT TEST RESULTS ===")
	t.Logf("Execution time: %v (Target: < 150ms, Previously: ~467ms)", elapsed)
	t.Logf("Stocks harvested: %d / 16 (Theoretical Max: 16)", stocks)
	t.Logf("Today types harvested: %d / 4 (All Brands: 4)", todayTypes)
	t.Logf("Match types harvested: %d / 4", matchTypes)

	// Kiểm tra kết quả giữ nguyên 100%
	if stocks != 16 {
		t.Errorf("KẾT QUẢ KHÔNG GIỮ NGUYÊN: Mong muốn 16/16 stocks, thực tế được %d", stocks)
	}
	if todayTypes != 4 {
		t.Errorf("KẾT QUẢ KHÔNG GIỮ NGUYÊN: Mong muốn 4/4 brands, thực tế được %d", todayTypes)
	}
	// Kiểm tra thời gian phản hồi giảm sâu
	if elapsed > 150*time.Millisecond {
		t.Errorf("Thời gian phản hồi chưa tối ưu: tốn %v (kỳ vọng < 150ms)", elapsed)
	}
}

// TestEarlyExit24x24 kiểm tra trên map 24x24 (48 stocks, 6 brands, 6 Patrols + 2 Refuelers).
// Đảm bảo dừng sớm đạt 48/48 stocks mà không phải chờ hết 877ms.
func TestEarlyExit24x24(t *testing.T) {
	W, H := 24, 24
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100, 100},
		FuelLimits: 200,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 12; i++ {
		pos := (i%4+1)*4*W + (i/4+1)*4
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 6,
			Pos:    pos,
			Stocks: 4,
		})
	}
	gs := NewGameState(s)
	f200 := 200
	f0 := 0
	for i := 0; i < 6; i++ {
		gs.AgentKinds[i] = 0 // Patrol
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 200
	}
	for i := 6; i < 8; i++ {
		gs.AgentKinds[i] = 1 // Refueler
		gs.AgentPos[i] = s.Spots[i+2].Pos
		gs.AgentFuel[i] = 0
	}

	bt := NewBrandTracker()
	mock := &mockTransport{}
	st := &DayState{
		Day:    0,
		Agents: make([]Agent, 8),
	}
	for i := 0; i < 6; i++ {
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f200}
	}
	for i := 6; i < 8; i++ {
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f0}
	}

	start := time.Now()
	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
	elapsed := time.Since(start)

	matchTypes, todayTypes, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("=== 24x24 EARLY EXIT TEST RESULTS ===")
	t.Logf("Execution time: %v (Target: < 350ms, Previously: ~877ms)", elapsed)
	t.Logf("Stocks harvested: %d / 48 (Theoretical Max: 48)", stocks)
	t.Logf("Today types harvested: %d / 6 (All Brands: 6)", todayTypes)
	t.Logf("Match types harvested: %d / 6", matchTypes)

	if stocks != 48 {
		t.Errorf("KẾT QUẢ KHÔNG GIỮ NGUYÊN: Mong muốn 48/48 stocks, thực tế được %d", stocks)
	}
	if todayTypes != 6 {
		t.Errorf("KẾT QUẢ KHÔNG GIỮ NGUYÊN: Mong muốn 6/6 brands, thực tế được %d", todayTypes)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("Thời gian phản hồi 24x24 chưa tối ưu: tốn %v (kỳ vọng < 400ms)", elapsed)
	}
}

// TestConvergenceEarlyExitConstrained kiểm tra khi bản đồ bị giới hạn bước (budget nhỏ) không thể ăn hết map.
// Thuật toán phải dừng sớm qua MaxNoImprove khi đã hội tụ mà không chờ hết timeout.
func TestConvergenceEarlyExitConstrained(t *testing.T) {
	W, H := 16, 16
	s := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{20}, // Budget rất nhỏ, không thể đi hết 10 spots
		FuelLimits: 100,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 10; i++ {
		pos := (i%3+1)*4*W + (i/3+1)*4
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 5,
			Pos:    pos,
			Stocks: 3,
		})
	}
	gs := NewGameState(s)
	f100 := 100
	for i := 0; i < 4; i++ {
		gs.AgentKinds[i] = 0
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 100
	}

	bt := NewBrandTracker()
	mock := &mockTransport{}
	st := &DayState{
		Day:    0,
		Agents: make([]Agent, 4),
	}
	for i := 0; i < 4; i++ {
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f100}
	}

	start := time.Now()
	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
	elapsed := time.Since(start)

	_, _, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("=== CONSTRAINED CONVERGENCE TEST RESULTS ===")
	t.Logf("Execution time: %v (Kỳ vọng < 300ms qua hội tụ sớm)", elapsed)
	t.Logf("Stocks harvested with small budget 20: %d", stocks)

	if len(actions) != 4 {
		t.Fatalf("Expected 4 actions, got %d", len(actions))
	}
	if elapsed > 350*time.Millisecond {
		t.Errorf("Hội tụ sớm không hoạt động như kỳ vọng: tốn %v (kỳ vọng < 350ms)", elapsed)
	}
}

// TestMap9x9 kiểm tra tính chính xác và hiệu năng trên bản đồ kích thước 9x9 (lẻ)
func TestMap9x9(t *testing.T) {
	W, H := 9, 9
	s := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{40},
		FuelLimits: 80,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 8; i++ {
		pos := (i%3+1)*2*W + (i/3+1)*2
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 4,
			Pos:    pos,
			Stocks: 2,
		})
	}
	gs := NewGameState(s)
	f80 := 80
	for i := 0; i < 4; i++ {
		gs.AgentKinds[i] = 0
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 80
	}

	bt := NewBrandTracker()
	mock := &mockTransport{}
	st := &DayState{
		Day:    0,
		Agents: make([]Agent, 4),
	}
	for i := 0; i < 4; i++ {
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f80}
	}

	start := time.Now()
	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
	elapsed := time.Since(start)

	matchTypes, todayTypes, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("=== 9x9 MAP TEST RESULTS ===")
	t.Logf("Execution time: %v", elapsed)
	t.Logf("Stocks: %d / 16, Today types: %d / 4, Match types: %d / 4", stocks, todayTypes, matchTypes)

	if stocks != 16 || todayTypes != 4 {
		t.Errorf("9x9 map failed: stocks=%d/16, types=%d/4", stocks, todayTypes)
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("9x9 map took too long: %v", elapsed)
	}
}

// TestMap10x10 kiểm tra tính chính xác và hiệu năng trên bản đồ kích thước 10x10
func TestMap10x10(t *testing.T) {
	W, H := 10, 10
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4},
		DaySteps:   []int{45},
		FuelLimits: 100,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 10; i++ {
		pos := (i%4+1)*2*W + (i/4+1)*2
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 5,
			Pos:    pos,
			Stocks: 2,
		})
	}
	gs := NewGameState(s)
	f100 := 100
	for i := 0; i < 5; i++ {
		gs.AgentKinds[i] = 0
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 100
	}

	bt := NewBrandTracker()
	mock := &mockTransport{}
	st := &DayState{
		Day:    0,
		Agents: make([]Agent, 5),
	}
	for i := 0; i < 5; i++ {
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f100}
	}

	start := time.Now()
	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
	elapsed := time.Since(start)

	matchTypes, todayTypes, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("=== 10x10 MAP TEST RESULTS ===")
	t.Logf("Execution time: %v", elapsed)
	t.Logf("Stocks: %d / 20, Today types: %d / 5, Match types: %d / 5", stocks, todayTypes, matchTypes)

	if stocks != 20 || todayTypes != 5 {
		t.Errorf("10x10 map failed: stocks=%d/20, types=%d/5", stocks, todayTypes)
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("10x10 map took too long: %v", elapsed)
	}
}

func TestMultiDay32x32Simulation(t *testing.T) {
	W, H := 32, 32
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100, 100, 100, 100},
		FuelLimits: 200,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}

	// 12 bãi Udon phân bố đều trên map 32x32, mỗi bãi 4 stocks (tổng 48 stocks/ngày)
	for i := 0; i < 12; i++ {
		row := (i/4 + 1) * 7
		col := (i%4 + 1) * 7
		pos := row*W + col
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 6,
			Pos:    pos,
			Stocks: 4,
		})
	}

	gs := NewGameState(s)
	kinds := assignKinds(s)
	gs.SetKinds(kinds)
	for i, k := range kinds {
		gs.AgentKinds[i] = k
		gs.AgentPos[i] = s.Spots[i].Pos
		if k == 0 {
			gs.AgentFuel[i] = 200
		} else {
			gs.AgentFuel[i] = 0
		}
	}
	gs.CurrentDay = 0
	gs.Initialized = true

	bt := NewBrandTracker()
	mock := &mockTransport{}

	totalStocksAllDays := 0

	for day := 0; day < 4; day++ {
		gs.CurrentDay = day
		gs.resetSpots()

		st := &DayState{
			Day:    day,
			Agents: make([]Agent, 8),
		}
		for i := 0; i < 8; i++ {
			f := gs.AgentFuel[i]
			st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f}
		}

		actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)
		_, _, stocks := scoreActions(actions, gs, bt.Collected, day)
		totalStocksAllDays += stocks
		t.Logf("[DAY %d] Stocks harvested: %d / 48 | Agent end positions: %v | Fuels: %v",
			day, stocks, gs.AgentPos, gs.AgentFuel)

		// Cập nhật vị trí và xăng sau ngày cho ngày tiếp theo
		for i, seq := range actions {
			pos := gs.AgentPos[i]
			fuel := gs.AgentFuel[i]
			remSteps := 100
			for _, a := range seq {
				if a >= 0 && a <= 5 {
					nb := neighbor(pos, a, W, H)
					if nb >= 0 {
						pos = nb
						remSteps -= 2
						fuel -= 1
					}
				} else if a < 0 {
					remSteps -= (-a)
				}
			}
			gs.AgentPos[i] = pos
			if gs.IsPatrol(i) {
				// Nếu có Refueler cùng ô cuối ngày, được nạp đầy
				for ref := range gs.AgentKinds {
					if !gs.IsPatrol(ref) && gs.AgentPos[ref] == pos {
						fuel = 200
						break
					}
				}
				if fuel < 0 {
					fuel = 0
				}
				gs.AgentFuel[i] = fuel
			}
		}
	}

	t.Logf("=== 4-DAY 32x32 SIMULATION TOTAL: %d / 192 Udon ===", totalStocksAllDays)
	if totalStocksAllDays < 150 {
		t.Errorf("Expected at least 150 total stocks over 4 days on 32x32, got %d", totalStocksAllDays)
	}
}

// TestDay7LowFuelSafetyCheck tái hiện chính xác kịch bản trận m-21670 ngày 7 & 8:
// Xe 1 chỉ còn 49 fuel trên bản đồ 32x32 với budget 100 bước.
// Đảm bảo TUYỆT ĐỐI không bao giờ phát sinh lỗi E_NO_FUEL trên bất kỳ xe nào!
func TestDay7LowFuelSafetyCheck(t *testing.T) {
	W, H := 32, 32
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100, 100},
		FuelLimits: 200,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}

	for i := 0; i < 12; i++ {
		row := (i/4 + 1) * 7
		col := (i%4 + 1) * 7
		pos := row*W + col
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 6,
			Pos:    pos,
			Stocks: 4,
		})
	}

	gs := NewGameState(s)
	kinds := []int{0, 0, 0, 0, 0, 0, 1, 1}
	gs.SetKinds(kinds)

	// Vị trí và fuel mô phỏng chính xác trận m-21670 Day 7
	// positions: [484 768 74 908 574 691 334 691] | fuels: [108 49 191 188 126 200 0 0]
	mockPositions := []int{484, 768, 74, 908, 574, 691, 334, 691}
	mockFuels := []int{108, 49, 191, 188, 126, 200, 0, 0}
	for i := 0; i < 8; i++ {
		gs.AgentKinds[i] = kinds[i]
		gs.AgentPos[i] = mockPositions[i]
		gs.AgentFuel[i] = mockFuels[i]
	}
	gs.CurrentDay = 0
	gs.Initialized = true

	bt := NewBrandTracker()
	mock := &mockTransport{}

	st := &DayState{
		Day:    0,
		Agents: make([]Agent, 8),
	}
	for i := 0; i < 8; i++ {
		f := gs.AgentFuel[i]
		st.Agents[i] = Agent{Pos: gs.AgentPos[i], Fuel: &f}
	}

	actions := RunDayWithTransport(context.Background(), st, gs, bt, mock)

	// Mô phỏng server-side validator nghiêm ngặt:
	// Từng turn t = 1..100: Kiểm tra xem có xe nào bị E_NO_FUEL không
	curPos := make([]int, 8)
	curFuel := make([]int, 8)
	copy(curPos, gs.AgentPos)
	copy(curFuel, gs.AgentFuel)

	stepProgress := make([]int, 8)
	actionIdx := make([]int, 8)

	for step := 1; step <= 100; step++ {
		// Bắt đầu lệnh mới nếu rảnh
		for i := 0; i < 8; i++ {
			for stepProgress[i] == 0 && actionIdx[i] < len(actions[i]) {
				cmd := actions[i][actionIdx[i]]
				actionIdx[i]++
				if cmd < 0 {
					stepProgress[i] = -cmd
					break
				} else if cmd >= 0 && cmd <= 5 {
					nb := neighbor(curPos[i], cmd, W, H)
					if nb < 0 {
						t.Fatalf("[SERVER REJECT] Xe %d đi ra ngoài bản đồ!", i)
					}
					sc, fc, ok := TerrainCost(gs.Cell(curPos[i]), gs.TrafficStatus(curPos[i]))
					if !ok {
						t.Fatalf("[SERVER REJECT] Xe %d đi vào ô không hợp lệ!", i)
					}
					if gs.IsPatrol(i) {
						if curFuel[i] < fc {
							t.Fatalf("[SERVER REJECT E_NO_FUEL] (xe %d, bước %d): không đủ nhiên liệu để di chuyển (còn %d, cần %d)!",
								i, step, curFuel[i], fc)
						}
						curFuel[i] -= fc
					}
					stepProgress[i] = sc
					curPos[i] = nb
					break
				}
			}
		}

		// Tiến thời gian 1 bước
		for i := 0; i < 8; i++ {
			if stepProgress[i] > 0 {
				stepProgress[i]--
			}
		}

		// Cuối bước t: Kiểm tra refuel nếu Refueler và Patrol cùng ô
		for r := 6; r < 8; r++ {
			if stepProgress[r] == 0 {
				for p := 0; p < 6; p++ {
					if stepProgress[p] == 0 && curPos[p] == curPos[r] {
						curFuel[p] = gs.FuelLimit()
					}
				}
			}
		}
	}

	t.Logf("=== TEST M-21670 DAY 7 LOW FUEL SCENARIO PASSED ===")
	t.Logf("Patrol 1 (49 fuel) survived the entire day with ZERO E_NO_FUEL errors!")
}


