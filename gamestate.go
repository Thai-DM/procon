// gamestate.go — GameState: quản lý trạng thái trận đấu phía bot.
// Bot tự track kho udon nội bộ vì server KHÔNG trả kho hiện tại.
// Vị trí thật của agent lấy từ DayState ngày 0, KHÔNG từ Setup.Agents.
package main

// SpotState theo dõi kho udon nội bộ cho 1 điểm thu (spot).
// Server KHÔNG trả kho còn lại — bot phải tự quản lý.
type SpotState struct {
	Brand     int
	Pos       int
	MaxStocks int          // = Setup.Spots[i].Stocks, không đổi cả trận
	DayStocks int          // còn lại hôm nay; reset về MaxStocks đầu mỗi ngày
	Visited   map[int]bool // agentIdx → đã ghé hôm nay (Patrol chỉ thu được lần đầu/ngày)
}

// GameState lưu trữ toàn bộ trạng thái trận đấu phía bot.
// Cập nhật mỗi khi có DayState mới từ server.
type GameState struct {
	Setup       *Setup
	AgentPos    []int       // vị trí hiện tại của từng agent
	AgentFuel   []int       // fuel hiện tại (luôn 0 với Refueler)
	AgentKinds  []int       // kind cố định cả trận, ghi nhận sau PostAssignment
	Traffics    map[int]int // pos → status giao thông hôm nay (0/1/2)
	Spots       []SpotState
	CurrentDay  int
	Initialized bool // true sau khi InitDay0 được gọi
}

// NewGameState tạo GameState mới từ Setup sau khi lấy cấu hình.
func NewGameState(s *Setup) *GameState {
	spots := make([]SpotState, len(s.Spots))
	for i, sp := range s.Spots {
		spots[i] = SpotState{
			Brand:     sp.Brand,
			Pos:       sp.Pos,
			MaxStocks: sp.Stocks,
			DayStocks: sp.Stocks,
			Visited:   make(map[int]bool),
		}
	}
	n := len(s.Agents)
	return &GameState{
		Setup:      s,
		AgentPos:   make([]int, n),
		AgentFuel:  make([]int, n),
		AgentKinds: make([]int, n),
		Traffics:   make(map[int]int),
		Spots:      spots,
		CurrentDay: -1,
	}
}

// SetKinds ghi nhớ loại agent đã gán (gọi ngay sau PostAssignment thành công).
func (gs *GameState) SetKinds(kinds []int) {
	n := len(kinds)
	if n > len(gs.AgentKinds) {
		n = len(gs.AgentKinds)
	}
	copy(gs.AgentKinds[:n], kinds)
}

// InitDay0 khởi tạo vị trí và fuel thật của agent từ DayState ngày 0.
// ⚠️  Vị trí xuất phát THẬT lấy từ DayState.Agents[i].Pos —
//
//	Setup.Agents chỉ là []int số lượng, KHÔNG có vị trí.
func (gs *GameState) InitDay0(st *DayState) {
	for i := range gs.AgentPos {
		if i >= len(st.Agents) {
			break
		}
		a := &st.Agents[i]
		gs.AgentPos[i] = a.Pos
		gs.AgentFuel[i] = gs.resolveFuel(i, a)
	}
	gs.applyTraffics(st)
	gs.resetSpots()
	gs.CurrentDay = 0
	gs.Initialized = true
}

// NewDay cập nhật state cho ngày mới (day > 0):
//   - Carry-over pos/fuel từ server (server trả giá trị cập nhật trong DayState)
//   - Reset kho udon về đầy cho mỗi spot (độc lập theo đội)
//   - Cập nhật bảng traffic mới từ server
func (gs *GameState) NewDay(st *DayState) {
	for i := range gs.AgentPos {
		if i >= len(st.Agents) {
			break
		}
		a := &st.Agents[i]
		gs.AgentPos[i] = a.Pos
		gs.AgentFuel[i] = gs.resolveFuel(i, a)
	}
	gs.applyTraffics(st)
	gs.resetSpots()
	gs.CurrentDay = st.Day
}

// RecordVisit ghi nhận Patrol agent agentIdx ghé spot spotIdx.
// Trả số stocks thu được (0 nếu đã ghé hôm nay hoặc kho trống).
// Patrol chỉ thu được ở LẦN GHÉ ĐẦU TIÊN mỗi spot mỗi ngày.
func (gs *GameState) RecordVisit(agentIdx, spotIdx int) int {
	if spotIdx < 0 || spotIdx >= len(gs.Spots) {
		return 0
	}
	sp := &gs.Spots[spotIdx]
	if sp.Visited[agentIdx] || sp.DayStocks <= 0 {
		return 0
	}
	sp.DayStocks--
	sp.Visited[agentIdx] = true
	return 1
}

// IsSpotAvailable kiểm tra spot còn hàng và agent chưa ghé hôm nay.
func (gs *GameState) IsSpotAvailable(agentIdx, spotIdx int) bool {
	if spotIdx < 0 || spotIdx >= len(gs.Spots) {
		return false
	}
	sp := &gs.Spots[spotIdx]
	return !sp.Visited[agentIdx] && sp.DayStocks > 0
}

// SpotIndexAt tìm index trong gs.Spots theo pos ô. Trả -1 nếu không có.
func (gs *GameState) SpotIndexAt(pos int) int {
	for i := range gs.Spots {
		if gs.Spots[i].Pos == pos {
			return i
		}
	}
	return -1
}

// TrafficStatus trả status giao thông tại pos (0 nếu không có dữ liệu / không phải đường).
func (gs *GameState) TrafficStatus(pos int) int {
	return gs.Traffics[pos]
}

// FuelOf trả fuel của agent (an toàn, 0 với Refueler).
func (gs *GameState) FuelOf(agentIdx int) int {
	if agentIdx < 0 || agentIdx >= len(gs.AgentFuel) {
		return 0
	}
	return gs.AgentFuel[agentIdx]
}

// IsPatrol kiểm tra agent có phải Patrol (kind=0).
func (gs *GameState) IsPatrol(agentIdx int) bool {
	if agentIdx < 0 || agentIdx >= len(gs.AgentKinds) {
		return false
	}
	return gs.AgentKinds[agentIdx] == 0
}

// DayStepsForDay trả ngân sách step cho ngày day.
// Fallback về 30 nếu day ngoài phạm vi (an toàn khi parse lỗi).
func (gs *GameState) DayStepsForDay(day int) int {
	if day >= 0 && day < len(gs.Setup.DaySteps) {
		return gs.Setup.DaySteps[day]
	}
	return 30
}

// Width, Height, Cell — accessor tiện lợi để tránh dereference nhiều lần.
func (gs *GameState) Width() int          { return gs.Setup.Map.Width }
func (gs *GameState) Height() int         { return gs.Setup.Map.Height }
func (gs *GameState) Cell(pos int) int    { return gs.Setup.Map.Cells[pos/gs.Width()][pos%gs.Width()] }
func (gs *GameState) FuelLimit() int      { return gs.Setup.FuelLimits }

// --- internal helpers ---

// resolveFuel lấy fuel an toàn từ Agent struct.
// Refueler trả 0, Patrol nil-fuel fallback về FuelLimits (full tank).
func (gs *GameState) resolveFuel(agentIdx int, a *Agent) int {
	if a.Kind == 1 {
		return 0 // Refueler không dùng fuel
	}
	if a.Fuel != nil {
		return *a.Fuel
	}
	// Patrol không có fuel data → giả định đầy bình (an toàn hơn là 0)
	return gs.Setup.FuelLimits
}

// applyTraffics reset và cập nhật bảng giao thông từ DayState mới.
func (gs *GameState) applyTraffics(st *DayState) {
	for k := range gs.Traffics {
		delete(gs.Traffics, k)
	}
	for _, t := range st.Traffics {
		gs.Traffics[t.Pos] = t.Status
	}
}

// resetSpots reset kho udon về đầy và xóa visited đầu ngày mới.
// Kho độc lập theo đội — mỗi đội thu riêng (không ảnh hưởng đội khác).
func (gs *GameState) resetSpots() {
	for i := range gs.Spots {
		gs.Spots[i].DayStocks = gs.Spots[i].MaxStocks
		for k := range gs.Spots[i].Visited {
			delete(gs.Spots[i].Visited, k)
		}
	}
}
