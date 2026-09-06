// encode.go — Chuyển đổi route (danh sách pos) thành action sequence []int cho /actions.
// Đảm bảo đúng format: hướng(0-5) hoặc WAIT(≤-1), cắt/đệm đúng DaySteps[day].
package main

// ---------------------------------------------------------------------------
// EncodeRoute chuyển route ([]pos) thành dãy hành động hợp lệ cho 1 agent.
//
// Quy tắc:
//   - Mỗi phần tử ∈ {0..5}: di chuyển theo hướng đó (tốn step/fuel theo địa hình nguồn)
//   - Phần tử ≤ -1: đứng yên N bước (WAIT -N)
//   - Tổng step tiêu thụ KHÔNG vượt budget
//   - Cuối dãy: đệm WAIT âm cho đủ budget (không để server tự đứng yên phần thiếu)
//   - Chèn WAIT tại điểm hẹn refuel nếu có (rendezvous)
//
// Trả dãy hành động sẵn sàng gửi /actions.
// ---------------------------------------------------------------------------

// WaitPoint mô tả 1 điểm dừng chờ refuel trên route.
type WaitPoint struct {
	Pos       int  // ô cần đứng yên
	WaitSteps int  // số step đứng yên (≥0)
	IsRefuel  bool // đánh dấu nếu điểm này là nơi xe Patrol được nạp xăng
}

// EncodeOpts chứa tham số encode cho 1 agent trong 1 ngày.
type EncodeOpts struct {
	Route       []int       // danh sách pos [from, ..., to]
	Budget      int         // DaySteps[day]
	StartFuel   int         // fuel còn lại đầu ngày
	IsRefueler  bool
	WaitPoints  []WaitPoint // điểm hẹn refuel (có thể rỗng)
	Gs          *GameState
}

// EncodeRoute chuyển route + wait points thành dãy hành động hợp lệ.
// Dừng sớm nếu hết step hoặc hết fuel, đệm WAIT cho đủ budget.
func EncodeRoute(opts EncodeOpts) []int {
	W, H := opts.Gs.Width(), opts.Gs.Height()

	// Map pos -> steps cần đứng yên tại đó và cờ nạp nhiên liệu
	waitAt := make(map[int]int)
	refuelAt := make(map[int]bool)
	for _, wp := range opts.WaitPoints {
		if wp.WaitSteps > 0 {
			waitAt[wp.Pos] = wp.WaitSteps
		}
		if wp.IsRefuel {
			refuelAt[wp.Pos] = true
		}
	}

	seq := make([]int, 0, opts.Budget)
	remSteps := opts.Budget
	remFuel := opts.StartFuel

	// Xử lý route theo từng cặp (from, to)
	for i := 0; i < len(opts.Route)-1 && remSteps > 0; i++ {
		from, to := opts.Route[i], opts.Route[i+1]

		// 1. Nếu có điểm hẹn tại `from`: thực hiện WAIT trước
		if w, ok := waitAt[from]; ok && w > 0 {
			actual := w
			if actual > remSteps {
				actual = remSteps
			}
			seq = append(seq, -actual)
			remSteps -= actual
			delete(waitAt, from)
			if remSteps == 0 {
				// Nếu đã hết toàn bộ bước sau khi WAIT và có cờ refuel, cập nhật refuel trước khi kết thúc
				if refuelAt[from] && !opts.IsRefueler {
					remFuel = opts.Gs.FuelLimit()
					delete(refuelAt, from)
				}
				break
			}
		}

		// 2. Nạp nhiên liệu tại điểm hẹn SAU KHI (hoặc KHI) đã hoàn tất WAIT tại ô này
		if refuelAt[from] && !opts.IsRefueler {
			remFuel = opts.Gs.FuelLimit()
			delete(refuelAt, from)
		}

		dir := dirTo(from, to, W, H)
		if dir < 0 {
			break // không kề nhau — route lỗi, dừng ở đây
		}

		// 3. Kiểm tra tính hợp lệ của bước di chuyển (kể cả fuel và step)
		res, aerr := ValidateMove(from, dir, remSteps, remFuel, opts.IsRefueler, opts.Gs)
		if aerr != nil {
			// Không đủ bước/fuel để tiếp tục — dừng an toàn (sẽ được đệm WAIT ở cuối)
			break
		}

		seq = append(seq, dir)
		remSteps -= res.StepConsumed
		remFuel -= res.FuelConsumed
	}

	// WAIT/Refuel tại đích cuối nếu có điểm hẹn ở đó
	if len(opts.Route) > 0 {
		last := opts.Route[len(opts.Route)-1]
		if w, ok := waitAt[last]; ok && w > 0 && remSteps > 0 {
			actual := w
			if actual > remSteps {
				actual = remSteps
			}
			seq = append(seq, -actual)
			remSteps -= actual
		}
		if refuelAt[last] && !opts.IsRefueler {
			remFuel = opts.Gs.FuelLimit()
			delete(refuelAt, last)
		}
	}

	// Đệm WAIT cho đủ budget (spec yêu cầu tự đệm, không để server tự đứng)
	if remSteps > 0 {
		seq = append(seq, -remSteps)
	}

	// Phòng vệ: luôn có ít nhất 1 phần tử
	if len(seq) == 0 {
		seq = []int{-opts.Budget}
	}

	return seq
}

// ---------------------------------------------------------------------------
// EncodePlan tạo [][]int toàn bộ cho cả đội từ danh sách AgentPlan.
// ---------------------------------------------------------------------------

// AgentPlan kế hoạch cho 1 agent trong ngày.
type AgentPlan struct {
	AgentIdx   int
	Route      []int       // [pos0, pos1, ...] — bắt đầu từ vị trí hiện tại
	WaitPoints []WaitPoint // điểm hẹn refuel dọc route
}

// EncodePlan tạo [][]int gửi /actions từ danh sách AgentPlan.
// AgentPlan không có trong slice → agent đứng yên cả ngày (an toàn).
func EncodePlan(plans []AgentPlan, day int, gs *GameState) [][]int {
	n := len(gs.AgentPos)
	budget := gs.DayStepsForDay(day)
	out := make([][]int, n)

	// Mặc định: tất cả đứng yên
	for i := range out {
		out[i] = []int{-budget}
	}

	for _, p := range plans {
		if p.AgentIdx < 0 || p.AgentIdx >= n {
			continue
		}
		isRef := !gs.IsPatrol(p.AgentIdx)
		route := p.Route
		if len(route) == 0 {
			// Không có route — giữ mặc định đứng yên
			continue
		}

		startFuel := gs.FuelOf(p.AgentIdx)
		if !isRef {
			for rIdx, k := range gs.AgentKinds {
				if k == 1 && gs.AgentPos[rIdx] == gs.AgentPos[p.AgentIdx] {
					startFuel = gs.FuelLimit()
					break
				}
			}
		}

		out[p.AgentIdx] = EncodeRoute(EncodeOpts{
			Route:      route,
			Budget:     budget,
			StartFuel:  startFuel,
			IsRefueler: isRef,
			WaitPoints: p.WaitPoints,
			Gs:         gs,
		})
	}

	return out
}

// ---------------------------------------------------------------------------
// Helpers tiện ích
// ---------------------------------------------------------------------------



// CountBudgetUsed tính tổng step thật tiêu thụ (dùng TerrainCost) từ seq + vị trí ban đầu.
// Chính xác hơn CountSteps — dùng để validate trước submit.
func CountBudgetUsed(seq []int, startPos int, gs *GameState) int {
	pos := startPos
	total := 0
	W, H := gs.Width(), gs.Height()
	for _, a := range seq {
		if a >= 0 && a <= 5 {
			nb := neighbor(pos, a, W, H)
			if nb < 0 {
				break
			}
			sc, _, canMove := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
			if !canMove {
				break
			}
			total += sc
			pos = nb
		}
		// Bỏ qua lệnh wait (a < 0) vì chúng ta muốn đếm số bước đi "thực tế" (active steps)
		// để tối ưu fuel / thời gian rảnh.
	}
	return total
}

// CountBudgetUsedAll tính tổng step tiêu thụ của tất cả các xe (dùng để tie-breaker).
func CountBudgetUsedAll(actions [][]int, gs *GameState) int {
	total := 0
	for i, seq := range actions {
		if i >= len(gs.AgentPos) {
			break
		}
		total += CountBudgetUsed(seq, gs.AgentPos[i], gs)
	}
	return total
}
