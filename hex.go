// hex.go — Hình học lục giác Even-R, chi phí địa hình, và validation hành động.
// Tất cả hàm nhận W, H, cells qua GameState để tránh global mutable state.
package main

import "fmt"

// ---------------------------------------------------------------------------
// Hướng di chuyển (thuận chiều kim đồng hồ từ trên-trái):
//   0=trên-trái  1=trên-phải  2=phải  3=dưới-phải  4=dưới-trái  5=trái
//
// Hệ tọa độ Even-R: hàng chẵn (r%2==0) lệch phải.
// dEven = bù lệch cho hàng CHẴN, dOdd = bù lệch cho hàng LẺ.
// Bố cục [6][2]{dCol, dRow} — khớp chính xác code mẫu BTC.
// ---------------------------------------------------------------------------

var dEven = [6][2]int{
	{0, -1},  // 0: trên-trái
	{1, -1},  // 1: trên-phải
	{1, 0},   // 2: phải
	{1, 1},   // 3: dưới-phải
	{0, 1},   // 4: dưới-trái
	{-1, 0},  // 5: trái
}

var dOdd = [6][2]int{
	{-1, -1}, // 0: trên-trái
	{0, -1},  // 1: trên-phải
	{1, 0},   // 2: phải
	{0, 1},   // 3: dưới-phải
	{-1, 1},  // 4: dưới-trái
	{-1, 0},  // 5: trái
}

// neighbor trả pos của ô kề theo hướng d từ pos.
// Trả -1 nếu ra ngoài biên bản đồ.
// W, H đọc từ GameState để tránh global mutable state.
func neighbor(pos, d, W, H int) int {
	r, c := pos/W, pos%W
	dl := dEven
	if r%2 == 1 {
		dl = dOdd
	}
	nc2, nr := c+dl[d][0], r+dl[d][1]
	if nc2 < 0 || nc2 >= W || nr < 0 || nr >= H {
		return -1
	}
	return nr*W + nc2
}

// dirTo tìm hướng d (0-5) để đi từ ô from sang ô to (phải kề nhau).
// Trả -1 nếu hai ô không kề nhau.
func dirTo(from, to, W, H int) int {
	for d := 0; d < 6; d++ {
		if neighbor(from, d, W, H) == to {
			return d
		}
	}
	return -1
}

// neighbors trả tất cả ô kề hợp lệ của pos (không ra ngoài biên, không phải ao).
// Dùng để build graph cho Dijkstra.
func neighbors(pos, W, H int, cells [][]int) []int {
	result := make([]int, 0, 6)
	for d := 0; d < 6; d++ {
		nb := neighbor(pos, d, W, H)
		if nb < 0 {
			continue
		}
		if cells[nb/W][nb%W] == 3 { // ao — không thể đi vào
			continue
		}
		result = append(result, nb)
	}
	return result
}

// ---------------------------------------------------------------------------
// Chi phí địa hình — Bảng 1 (tính theo ô NGUỒN trước khi rời)
//
// Trả (stepCost, fuelCost, canMove).
//   - canMove=false: ô ao (kind=3), không thể rời.
//   - fuelCost cho Refueler luôn là 0 bất kể địa hình — caller tự xử lý.
//   - trafficStatus: 0=thông 1=đông 2=tắc — chỉ dùng khi terrain==1 (đường).
// ---------------------------------------------------------------------------

// TerrainCost trả chi phí di chuyển RỜI ô src cho Patrol.
// Refueler: fuelCost luôn 0, chỉ stepCost có ý nghĩa.
func TerrainCost(srcCell, trafficStatus int) (stepCost, fuelCost int, canMove bool) {
	switch srcCell {
	case 0: // Đất / Đồng bằng
		return 2, 1, true
	case 2: // Núi
		return 3, 2, true
	case 1: // Đường — chi phí tùy traffic
		switch trafficStatus {
		case 0: // Thông thoáng
			return 1, 2, true
		case 1: // Đông đúc
			return 2, 2, true
		case 2: // Ùn tắc
			return 4, 2, true
		default:
			return 1, 2, true // fallback về thông thoáng
		}
	case 3: // Ao — không thể di chuyển
		return 0, 0, false
	default:
		return 0, 0, false // unknown — an toàn nhất là không đi
	}
}

// ---------------------------------------------------------------------------
// Validation hành động — kiểm tra trước khi encode thành action sequence.
// Các mã lỗi khớp với spec API 2.3.
// ---------------------------------------------------------------------------

// ActionError biểu diễn lỗi validation hành động với mã lỗi BTC.
type ActionError struct {
	Code string // E_NOT_ADJACENT | E_POND | E_STEP_OVERFLOW | E_NO_FUEL
	Msg  string
}

func (e *ActionError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Msg)
}

// MoveResult chứa kết quả của 1 bước di chuyển hợp lệ.
type MoveResult struct {
	NextPos       int
	StepConsumed  int
	FuelConsumed  int // 0 với Refueler
}

// ValidateMove kiểm tra 1 bước di chuyển theo hướng dir từ pos.
// isRefueler=true → fuelConsumed luôn 0.
// Trả MoveResult nếu hợp lệ, hoặc *ActionError với mã lỗi đúng spec.
func ValidateMove(
	pos, dir int,
	remainingSteps, remainingFuel int,
	isRefueler bool,
	gs *GameState,
) (*MoveResult, *ActionError) {
	W, H := gs.Width(), gs.Height()

	// Kiểm tra ô kề
	next := neighbor(pos, dir, W, H)
	if next < 0 {
		return nil, &ActionError{
			Code: "E_NOT_ADJACENT",
			Msg:  fmt.Sprintf("dir %d from pos %d goes out of bounds", dir, pos),
		}
	}

	// Kiểm tra ô đích có phải ao không
	destCell := gs.Cell(next)
	if destCell == 3 {
		return nil, &ActionError{
			Code: "E_POND",
			Msg:  fmt.Sprintf("destination pos %d is a pond", next),
		}
	}

	// Chi phí dựa trên ô NGUỒN (pos) theo đúng luật chính thức
	srcCell := gs.Cell(pos)
	trafficStatus := gs.TrafficStatus(pos)
	stepCost, fuelCost, canMove := TerrainCost(srcCell, trafficStatus)
	if !canMove {
		// Ô đích là ao — không thể rời (trường hợp đặc biệt, không xảy ra nếu state đúng)
		return nil, &ActionError{
			Code: "E_POND",
			Msg:  fmt.Sprintf("target pos %d is a pond", next),
		}
	}

	// Refueler không tốn fuel
	if isRefueler {
		fuelCost = 0
	}

	// Kiểm tra ngân sách step
	if remainingSteps < stepCost {
		return nil, &ActionError{
			Code: "E_STEP_OVERFLOW",
			Msg:  fmt.Sprintf("need %d steps, only %d remaining", stepCost, remainingSteps),
		}
	}

	// Kiểm tra fuel (chỉ Patrol)
	if !isRefueler && remainingFuel < fuelCost {
		return nil, &ActionError{
			Code: "E_NO_FUEL",
			Msg:  fmt.Sprintf("need %d fuel, only %d remaining", fuelCost, remainingFuel),
		}
	}

	return &MoveResult{
		NextPos:      next,
		StepConsumed: stepCost,
		FuelConsumed: fuelCost,
	}, nil
}

// SimulateRoute mô phỏng toàn bộ route (danh sách pos) và trả tổng chi phí.
// Dừng tại bước đầu tiên không hợp lệ.
// Trả (steps dùng, fuel dùng, số ô đi được thành công).
func SimulateRoute(
	route []int, // [pos0, pos1, pos2, ...] — pos0 là vị trí hiện tại
	startSteps, startFuel int,
	isRefueler bool,
	gs *GameState,
) (usedSteps, usedFuel, reached int) {
	if len(route) < 2 {
		return 0, 0, 0
	}
	W, H := gs.Width(), gs.Height()
	remSteps, remFuel := startSteps, startFuel
	for i := 0; i < len(route)-1; i++ {
		from, to := route[i], route[i+1]
		dir := dirTo(from, to, W, H)
		if dir < 0 {
			break // không kề nhau — route lỗi
		}
		res, aerr := ValidateMove(from, dir, remSteps, remFuel, isRefueler, gs)
		if aerr != nil {
			break
		}
		remSteps -= res.StepConsumed
		remFuel -= res.FuelConsumed
		reached = i + 1
	}
	usedSteps = startSteps - remSteps
	usedFuel = startFuel - remFuel
	return
}
