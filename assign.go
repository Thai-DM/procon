// assign.go — Task Assignment: DP Exact (≤ ngưỡng) / Cheapest Insertion + 2-opt + Lookahead.
// Tính lịch hẹn Refuel (Rendezvous Scheduling) dựa trên fuel dự đoán dọc route.
package main

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"time"
)

const (
	dpThreshold   = 15                     // số spot ứng viên tối đa để dùng DP exact (DP Bitmask O(2^N * N) hoàn thành trong <2ms với N=15)
	assignTimeout = 50 * time.Millisecond  // safety net - Global Assign is O(n^2) và nhanh

	// rclMinMargin: biên độ TUYỆT ĐỐI tối thiểu khi lọc Restricted Candidate List (RCL).
	// FIX QUAN TRỌNG: trước đây RCL lọc theo tỉ lệ % (val >= 0.7*bestVal), nhưng khi bestVal
	// nhỏ (map ít stock, hoặc sau khi hết brand mới trong ngày) biên độ % co lại gần 0,
	// khiến RCL chỉ còn ĐÚNG 1 phần tử — GRASP mất khả năng random hoá, mọi vòng lặp
	// AssignDay ra cùng 1 kết quả dù chạy hàng nghìn lần (tốn compute vô ích).
	// Dùng biên độ = max(rclMinMargin, tỉ lệ % của bestVal) để đảm bảo RCL luôn có
	// ít nhất vài ứng viên gần nhau, ngay cả khi giá trị tuyệt đối nhỏ.
	rclMinMargin = 0.08 // biên độ tối thiểu (phù hợp khi bestVal ~ 1.0)
	rclAlpha     = 0.15 // tỉ lệ % dùng khi |bestVal| lớn (e.g. khi có newBrandBonus)
)

// getStepCostFactor trả về hệ số phạt bước đi thích ứng từ MapProfile.
func getStepCostFactor(prof *MapProfile, baseFactor float64) float64 {
	return prof.StepFactor
}

// selectDiverseCandidates chọn lọc limit ứng viên tốt nhất, đảm bảo luôn bao gồm các bãi mang Brand mới.
func selectDiverseCandidates(cands []Candidate, limit int) []Candidate {
	if len(cands) <= limit {
		return cands
	}
	out := make([]Candidate, 0, limit)
	newTypeCands := make([]Candidate, 0)
	normalCands := make([]Candidate, 0)

	for _, c := range cands {
		if c.IsNewType {
			newTypeCands = append(newTypeCands, c)
		} else {
			normalCands = append(normalCands, c)
		}
	}

	for _, c := range newTypeCands {
		if len(out) >= limit {
			break
		}
		out = append(out, c)
	}
	for _, c := range normalCands {
		if len(out) >= limit {
			break
		}
		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Candidate — spot ứng viên cho 1 Patrol agent trong ngày.
// ---------------------------------------------------------------------------

// Candidate là 1 spot ứng viên với thông tin giá trị và chi phí.
type Candidate struct {
	SpotIdx   int
	Pos       int
	Brand     int
	IsNewType bool    // true nếu Brand này chưa từng thu (ưu tiên cao nhất)
	Value     float64 // giá trị thực tế của spot (số MaxStocks)
	Heuristic float64 // điểm số mồi heuristic (để sort cands ban đầu)
	StepCost  int     // step từ vị trí hiện tại của agent
	FuelCost  int     // fuel tiêu tốn (Patrol)
}

// buildCandidates tạo danh sách spot ứng viên cho 1 Patrol agent.
// Loại bỏ spot không khả thi (không tới được, đã thu hôm nay, hết kho).
func buildCandidates(
	agentIdx int,
	budget, fuelRemain int,
	localCollectedBrands map[int]bool,
	depletedSpots map[int]bool,
	gs *GameState,
	dc *DayDistCaches,
	prof *MapProfile,
) []Candidate {
	agentPos := gs.AgentPos[agentIdx]
	var cands []Candidate

	for i, sp := range gs.Spots {
		if !gs.IsSpotAvailable(agentIdx, i) || depletedSpots[i] {
			continue
		}
		p, ok := dc.Patrol.Get(agentPos, sp.Pos)
		if !ok || p.Steps > budget {
			continue // không tới được trong ngân sách
		}
		hasRefueler := false
		for _, k := range gs.AgentKinds {
			if k == 1 {
				hasRefueler = true
				break
			}
		}
		if !hasRefueler && p.Fuel > fuelRemain {
			continue // hết fuel trước khi tới (chỉ lọc khi KHÔNG có Refueler)
		}
		refDist := 0
		for rIdx, kind := range gs.AgentKinds {
			if kind == 1 { // Refueler
				if rp, ok2 := dc.Refueler.Get(gs.AgentPos[rIdx], sp.Pos); ok2 {
					refDist = rp.Steps
				}
				break
			}
		}

		isNew := !localCollectedBrands[sp.Brand]
		effectiveValue := 1.0
		if p.Steps > 3 {
			effectiveValue -= float64(p.Steps-3) * 0.05
			if effectiveValue < 0.2 {
				effectiveValue = 0.2
			}
		}
		stepFactor := prof.StepFactor
		heur := effectiveValue - float64(p.Steps)*stepFactor - float64(refDist)*0.02
		if isNew {
			heur += prof.NewBrandBonus
		}
		cands = append(cands, Candidate{
			SpotIdx:   i,
			Pos:       sp.Pos,
			Brand:     sp.Brand,
			IsNewType: isNew,
			Value:     1.0,
			Heuristic: heur,
			StepCost:  p.Steps,
			FuelCost:  p.Fuel,
		})
	}

	// Sort giảm dần theo Heuristic
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].Heuristic > cands[j].Heuristic
	})
	return cands
}

// dfsExact tìm tập spot + thứ tự thăm tối ưu cho 1 agent bằng đệ quy vét cạn (hoàn toàn chính xác).
// Đảm bảo không bị rớt spot do lỗi ghi đè trạng thái của DP cũ.
func dfsExact(
	ctx context.Context,
	agentIdx int,
	budget, fuelRemain int,
	cands []Candidate,
	collectedBrands map[int]bool,
	gs *GameState,
	dc *DayDistCaches,
	todayBrands map[int]bool,
	prof *MapProfile,
) (bestRoute []int, bestVal float64) {
	n := len(cands)
	if n == 0 {
		return nil, 0
	}
	if n > prof.DpThreshold {
		n = prof.DpThreshold
		cands = selectDiverseCandidates(cands, prof.DpThreshold)
	}

	agentPos := gs.AgentPos[agentIdx]

	// 1. Khởi tạo bestRoute & bestVal bằng nghiệm Greedy trước để đảm bảo an toàn tuyệt đối
	// Nếu DFS bị cắt giữa chừng (timeout), nghiệm trả về luôn KHÔNG THỂ TỆ HƠN GREEDY.
	greedyR := greedyCoverage(ctx, agentIdx, budget, fuelRemain, cands, collectedBrands, gs, dc, todayBrands, prof)
	bestRoute = make([]int, len(greedyR))
	copy(bestRoute, greedyR)
	bestVal = routeValue(bestRoute, cands, collectedBrands, agentPos, dc, gs, todayBrands, prof)

	bestSteps := 1 << 30

	// 1b. Tính trước max values cho Branch-and-Bound Upper Bound Pruning
	maxCandVal := make([]float64, n)
	for i, c := range cands {
		v := c.Value
		if !collectedBrands[c.Brand] {
			v += 10.0
		}
		maxCandVal[i] = v
	}

	// 2. Bảng Memoization DP Bitmask: memo[mask][lastCandIdx] ghi lại trạng thái đã từng đạt tới
	type memoState struct {
		val   float64
		steps int
		fuel  int
		ok    bool
	}
	memo := make(map[int]map[int]memoState)

	var search func(mask, lastCandIdx, currentSteps, currentFuel int, route []int)
	search = func(mask, lastCandIdx, currentSteps, currentFuel int, route []int) {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 1. Tính curVal của route hiện tại trước để so sánh với Memo
		curVal := routeValue(route, cands, collectedBrands, agentPos, dc, gs, todayBrands, prof)

		// 2. MEMOIZATION LOOKUP: So sánh trạng thái HIỆN TẠI với trạng thái QUÁ KHỨ (cùng mask, cùng điểm kết thúc)
		if memo[mask] != nil {
			if st, exists := memo[mask][lastCandIdx]; exists && st.ok {
				// Nếu con đường cũ tốn ít/bằng bước, ít/bằng tốn xăng, và điểm >= hiện tại -> CẮT NHÁNH!
				if st.steps <= currentSteps && st.fuel <= currentFuel && st.val >= curVal {
					return
				}
			}
		}

		// 3. Cập nhật Global Best Route
		if curVal > bestVal || (curVal == bestVal && currentSteps < bestSteps) {
			bestVal = curVal
			bestSteps = currentSteps
			bestRoute = make([]int, len(route))
			copy(bestRoute, route)
		}

		// 4. Lưu lại Memoization State mới
		if memo[mask] == nil {
			memo[mask] = make(map[int]memoState)
		}
		memo[mask][lastCandIdx] = memoState{val: curVal, steps: currentSteps, fuel: currentFuel, ok: true}

		if len(route) == n {
			return
		}

		// 5. Branch & Bound Pruning (Dùng curVal đã tính)
		remUpper := 0.0
		for i := 0; i < n; i++ {
			if (mask & (1 << i)) == 0 {
				remUpper += maxCandVal[i]
			}
		}
		if curVal+remUpper <= bestVal {
			return // Không thể vượt qua bestVal hiện tại, tỉa nhánh ngay
		}

		lastPos := agentPos
		if lastCandIdx >= 0 {
			lastPos = cands[lastCandIdx].Pos
		}

		// Nhánh thử tất cả các bước tiếp theo
		for next := 0; next < n; next++ {
			if (mask & (1 << next)) != 0 {
				continue
			}
			c := cands[next]
			p, ok := dc.Patrol.Get(lastPos, c.Pos)
			if !ok {
				continue
			}
			costSteps := p.Steps
			if lastPos == c.Pos {
				costSteps += 1
			}
			ns := currentSteps + costSteps
			nf := currentFuel + p.Fuel
			if ns > budget || nf > fuelRemain {
				continue
			}
			route = append(route, next)
			search(mask|(1<<next), next, ns, nf, route)
			route = route[:len(route)-1]
		}
	}

	search(0, -1, 0, 0, make([]int, 0, n))

	return bestRoute, bestVal
}

// ---------------------------------------------------------------------------
// Heuristic: Greedy Maximum Coverage — fallback khi > dpThreshold spots.
// ---------------------------------------------------------------------------

// greedyCoverage chọn spot tham lam theo tỉ lệ value/cost, time-boxed.
func greedyCoverage(
	ctx context.Context,
	agentIdx int,
	budget, fuelRemain int,
	cands []Candidate,
	collectedBrands map[int]bool,
	gs *GameState,
	dc *DayDistCaches,
	todayBrands map[int]bool,
	prof *MapProfile,
) []int {
	agentPos := gs.AgentPos[agentIdx]
	matchBrands := copyBrands(collectedBrands)
	tBrands := copyBrands(todayBrands)
	visitedSpots := make(map[int]bool, len(cands))
	var route []int
	remSteps := budget
	remFuel := fuelRemain
	curPos := agentPos

	for {
		select {
		case <-ctx.Done():
			return route
		default:
		}

		best := -1
		bestV := -1.0
		for i, c := range cands {
			if visitedSpots[c.SpotIdx] {
				continue
			}
			p, ok := dc.Patrol.Get(curPos, c.Pos)
			if !ok {
				continue
			}
			stepCost := p.Steps
			if curPos == c.Pos {
				stepCost += 1
			}
			if stepCost > remSteps || p.Fuel > remFuel {
				continue
			}
			effectiveValue := 1.0
			if p.Steps > 3 {
				effectiveValue -= float64(p.Steps-3) * 0.05
				if effectiveValue < 0.2 {
					effectiveValue = 0.2
				}
			}
			v := effectiveValue - float64(p.Steps)*prof.StepFactor
			isNewInMatch := !matchBrands[c.Brand]
			isNewToday := !tBrands[c.Brand]
			if isNewInMatch {
				v += prof.NewBrandBonus
			} else if isNewToday {
				v += 0.5
			}
			if v > bestV {
				bestV = v
				best = i
			}
		}
		if best < 0 {
			break
		}
		c := cands[best]
		p, _ := dc.Patrol.Get(curPos, c.Pos)
		remSteps -= p.Steps
		remFuel -= p.Fuel
		curPos = c.Pos
		visitedSpots[c.SpotIdx] = true
		matchBrands[c.Brand] = true
		tBrands[c.Brand] = true
		route = append(route, best)
	}
	return route
}

// ---------------------------------------------------------------------------
// 2-opt improvement — cải thiện thứ tự thăm trong route heuristic.
// ---------------------------------------------------------------------------

// twoOpt cải thiện route bằng 2-opt, time-boxed bởi ctx.
func twoOpt(ctx context.Context, route []int, cands []Candidate, agentPos int, dc *DayDistCaches, gs *GameState, todayBrands map[int]bool) []int {
	if len(route) < 2 {
		return route
	}
	if len(route) == 2 {
		cost01 := routeCostSteps(route, cands, agentPos, dc)
		rev := []int{route[1], route[0]}
		cost10 := routeCostSteps(rev, cands, agentPos, dc)
		if cost10 < cost01 {
			return rev
		}
		return route
	}
	best := make([]int, len(route))
	copy(best, route)
	improved := true
	for improved {
		improved = false
		for i := 0; i < len(best)-1; i++ {
			for j := i + 1; j < len(best); j++ {
				select {
				case <-ctx.Done():
					return best
				default:
				}
				// Thử đảo đoạn [i..j]
				newRoute := reverseSegment(best, i, j)
				if routeCostSteps(newRoute, cands, agentPos, dc) < routeCostSteps(best, cands, agentPos, dc) {
					copy(best, newRoute)
					improved = true
				}
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Rendezvous Scheduling — tính điểm hẹn + số step chờ cho Refueler.
// ---------------------------------------------------------------------------

// RendezvousPlan kế hoạch hẹn nạp fuel giữa 1 Patrol và 1 Refueler.
type RendezvousPlan struct {
	PatrolIdx    int  // index agent Patrol
	MeetPos      int  // ô hẹn
	MeetStep     int  // step của Patrol khi tới meetPos (urgency: nhỏ = khẩn cấp hơn)
	RefSteps     int  // số step Refueler cần để đi từ curPos → meetPos (có thể vượt budget)
	PatrolWait   int  // số step Patrol đứng yên tại MeetPos (≥ 0)
	RefuelerWait int  // số step Refueler đứng yên tại MeetPos (≥ 1)
	IsComplete   bool // true nếu Refueler TỚI ĐƯỢC meetPos trong ngày hôm nay
	RefuelStep   int  // bước thứ mấy trong ngày thì xe Patrol được đổ đầy xăng
}

// planRendezvousFrom — FIX #1 + FIX #2
// FIX #1: Kiểm tra fuel TỪ gs.FuelOf trực tiếp, không suy từ route.
//   Xe fuel=0 có route rỗng → detection từ route hoàn toàn bỏ sót.
//   MeetPos = vị trí hiện tại của Patrol, meetStep = 0.
// FIX #2: elapsedSteps tích lũy qua từng chặng chuỗi.
//   refArrival = elapsedSteps + p.Steps (không phải p.Steps từ đầu ngày).
//
// ⚠️  LƯU Ý QUAN TRỌNG: MeetStep là TỔNG STEP-COST (theo Bảng 1: đồng bằng=2,
// núi=3, đường tùy traffic=1/2/4) tích lũy dọc plannedPatrolRoute — KHÔNG PHẢI
// số ô (hop) trong route. Do đó KHÔNG được dùng MeetStep làm index để cắt
// slice route (route là danh sách ô, mỗi phần tử cách nhau đúng 1 hop).
// Muốn tìm vị trí cắt route đúng, phải tìm theo giá trị MeetPos (xem chỗ
// dùng ở AssignDay, phần "Cắt route cũ tại đúng điểm hẹn").
func planRendezvousFrom(
	patrolIdx int,
	refCurPos int,
	refBudgetLeft int,
	elapsedSteps int, // bước đã tiêu lũy kế của Refueler trong ngày
	plannedPatrolRoute []int,
	gs *GameState,
	dc *DayDistCaches,
	prof *MapProfile,
) *RendezvousPlan {
	if refBudgetLeft <= 0 {
		return nil
	}
	currentFuel := gs.FuelOf(patrolIdx)
	budget := gs.DayStepsForDay(gs.CurrentDay)

	// Nếu Patrol cạn xăng nguy kịch (<= 15) hoặc lộ trình rỗng, điểm hẹn duy nhất an toàn là tại chỗ
	if currentFuel <= 15 || len(plannedPatrolRoute) <= 1 {
		if currentFuel <= prof.LowFuelRefuelThreshold {
			meetPos := gs.AgentPos[patrolIdx]
			meetStep := 0
			p, ok := dc.Refueler.Get(refCurPos, meetPos)
			if !ok {
				return nil
			}
			isComplete := (p.Steps+1 <= refBudgetLeft)
			patWait := 1
			refWait := 1
			refArrival := elapsedSteps + p.Steps
			if isComplete {
				patWait = refArrival + 4
				refWait = 4
			} else {
				patWait = 0
				refWait = 0
			}
			return &RendezvousPlan{
				PatrolIdx:    patrolIdx,
				MeetPos:      meetPos,
				MeetStep:     meetStep,
				RefSteps:     p.Steps,
				PatrolWait:   patWait,
				RefuelerWait: refWait,
				IsComplete:   isComplete,
				RefuelStep:   refArrival + refWait,
			}
		}
		return nil
	}

	// Duyệt dọc lộ trình của Patrol để tìm các điểm hẹn khả thi
	type candMeet struct {
		pos        int
		meetStep   int
		remFuel    int
		refSteps   int
		refArrival int
		patWait    int
		refWait    int
		cost       int
	}

	var feasibleMeets []candMeet
	remFuel := currentFuel
	accumulatedSteps := 0
	pos := plannedPatrolRoute[0]

	// Kiểm tra ô xuất phát trước
	if currentFuel <= prof.LowFuelRefuelThreshold {
		if p, ok := dc.Refueler.Get(refCurPos, pos); ok && p.Steps <= refBudgetLeft {
			refArr := elapsedSteps + p.Steps
			patW := refArr + 4
			refW := 4
			if p.Steps+refW <= refBudgetLeft && patW <= budget {
				feasibleMeets = append(feasibleMeets, candMeet{
					pos:        pos,
					meetStep:   0,
					remFuel:    currentFuel,
					refSteps:   p.Steps,
					refArrival: refArr,
					patWait:    patW,
					refWait:    refW,
					cost:       patW*5 + p.Steps,
				})
			}
		}
	}

	for i := 1; i < len(plannedPatrolRoute); i++ {
		nextPos := plannedPatrolRoute[i]
		sc, fc, ok := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
		if !ok {
			break
		}
		accumulatedSteps += sc
		remFuel -= fc
		pos = nextPos

		if remFuel < 0 || accumulatedSteps > budget {
			break // Patrol không đủ xăng hoặc bước để đi tới ô này
		}

		// Chỉ xem xét tiếp xăng khi lượng xăng tại ô này <= LowFuelRefuelThreshold
		// hoặc khi xăng đầu ngày đã thấp (currentFuel <= LowFuelRefuelThreshold)
		if remFuel <= prof.LowFuelRefuelThreshold || currentFuel <= prof.LowFuelRefuelThreshold {
			p, ok := dc.Refueler.Get(refCurPos, pos)
			if !ok || p.Steps > refBudgetLeft {
				continue
			}

			refArrival := elapsedSteps + p.Steps
			var patWait, refWait int
			if refArrival >= accumulatedSteps {
				patWait = (refArrival - accumulatedSteps) + 4
				refWait = 4
			} else {
				patWait = 4
				refWait = (accumulatedSteps - refArrival) + 4
			}

			// Đảm bảo đủ ngân sách cho cả Refueler và Patrol tại điểm hẹn
			if p.Steps+refWait <= refBudgetLeft && accumulatedSteps+patWait <= budget {
				// Cost ưu tiên:
				// 1. patWait nhỏ nhất (Patrol không phải đứng chờ lâu, có thể chạy tiếp)
				// 2. Refueler tốn ít bước
				// 3. Thưởng nếu đón nạp đúng lúc Patrol chuẩn bị cạn xăng
				cost := patWait*5 + p.Steps
				if remFuel <= 40 {
					cost -= 15
				}
				feasibleMeets = append(feasibleMeets, candMeet{
					pos:        pos,
					meetStep:   accumulatedSteps,
					remFuel:    remFuel,
					refSteps:   p.Steps,
					refArrival: refArrival,
					patWait:    patWait,
					refWait:    refWait,
					cost:       cost,
				})
			}
		}
	}

	if len(feasibleMeets) > 0 {
		// Chọn điểm hẹn có cost tối ưu nhất
		best := feasibleMeets[0]
		for _, cm := range feasibleMeets[1:] {
			if cm.cost < best.cost {
				best = cm
			}
		}

		refuelStep := best.refArrival + best.refWait
		if best.meetStep+best.patWait > refuelStep {
			refuelStep = best.meetStep + best.patWait
		}

		return &RendezvousPlan{
			PatrolIdx:    patrolIdx,
			MeetPos:      best.pos,
			MeetStep:     best.meetStep,
			RefSteps:     best.refSteps,
			PatrolWait:   best.patWait,
			RefuelerWait: best.refWait,
			IsComplete:   true,
			RefuelStep:   refuelStep,
		}
	}

	// Fallback: Nếu không tìm được điểm hẹn hoàn chỉnh trong ngày,
	// nhưng xe Patrol thực sự cần cứu (currentFuel <= LowFuelRefuelThreshold),
	// Refueler vẫn di chuyển tối đa về phía điểm cuối của Patrol để áp sát cứu cho ngày mai!
	if currentFuel <= prof.LowFuelRefuelThreshold {
		endPos := plannedPatrolRoute[len(plannedPatrolRoute)-1]
		p, ok := dc.Refueler.Get(refCurPos, endPos)
		refSteps := refBudgetLeft
		if ok && p.Steps < refSteps {
			refSteps = p.Steps
		}
		return &RendezvousPlan{
			PatrolIdx:    patrolIdx,
			MeetPos:      endPos,
			MeetStep:     accumulatedSteps,
			RefSteps:     refSteps,
			PatrolWait:   0,
			RefuelerWait: 0,
			IsComplete:   false,
			RefuelStep:   refBudgetLeft,
		}
	}

	return nil
}

// PlanRendezvous — wrapper tương thích ngược.
func PlanRendezvous(
	patrolIdx, refuelIdx int,
	budget int,
	plannedPatrolRoute []int,
	gs *GameState,
	dc *DayDistCaches,
	prof *MapProfile,
) *RendezvousPlan {
	return planRendezvousFrom(patrolIdx, gs.AgentPos[refuelIdx], budget, 0, plannedPatrolRoute, gs, dc, prof)
}

// ---------------------------------------------------------------------------
// AssignDay — entry point chính: gán route tối ưu cho tất cả agent trong ngày.
// ---------------------------------------------------------------------------

// DayAssignment kết quả gán route cho cả đội trong ngày.
type DayAssignment struct {
	Plans         []AgentPlan
	TotalNewTypes int
	TotalStocks   int
}

// AssignDay gán route tối ưu cho từng Patrol agent, tính Rendezvous cho Refueler.
// ctx: time-box cho heuristic (nhánh DP không bị giới hạn).
func AssignDay(
	ctx context.Context,
	budget int,
	collectedBrands map[int]bool, // brand đã thu toàn trận (readonly)
	gs *GameState,
	dc *DayDistCaches,
	rng *rand.Rand,
) DayAssignment {
	prof := GetMapProfile(gs)
	var plans []AgentPlan

	// Xác định danh sách Refuelers và Patrols
	var refuelIdxs []int
	var patrolIdxs []int
	for i := range gs.AgentKinds {
		if !gs.IsPatrol(i) {
			refuelIdxs = append(refuelIdxs, i)
		} else {
			patrolIdxs = append(patrolIdxs, i)
		}
	}

	// ------------------------------------------------------------------
	// Global Marginal-Gain Assignment (Parallel Cheapest Insertion + GRASP)
	// ------------------------------------------------------------------
	var patrolRoutes [][]int // route pos list cho từng Patrol (để PlanRendezvous)
	assignedToPatrol := make(map[int][]int) // patrolIdx -> danh sách SpotIdx
	curPos := make(map[int]int)
	todayBrands := make(map[int]bool)
	remBudget := make(map[int]int)
	remFuel := make(map[int]int)

	for _, a := range patrolIdxs {
		curPos[a] = gs.AgentPos[a]
		remBudget[a] = budget
		startFuel := gs.FuelOf(a)
		// Nếu Refueler đứng cùng ô với Patrol ngay đầu ngày → refuel xảy ra ngay bước 1.
		// Dùng FuelLimit để lập kế hoạch, tránh việc xe bị đánh giá là "chết" khi thực ra
		// sẽ được nạp đầy trước khi đi bước đầu tiên.
		for _, refIdx := range refuelIdxs {
			if gs.AgentPos[refIdx] == gs.AgentPos[a] {
				startFuel = gs.FuelLimit()
				break
			}
		}
		remFuel[a] = startFuel
	}

	localBrands := make(map[int]bool)
	for k, v := range collectedBrands {
		localBrands[k] = v
	}

	// Xác định các patrol có thể được tiếp xăng hôm nay (tối đa len(refuelIdxs) xe)
	canRefuelToday := make(map[int]bool)
	if len(refuelIdxs) > 0 && len(patrolIdxs) > 0 {
		type patUrgency struct {
			idx  int
			fuel int
		}
		var allPatrols []patUrgency
		for _, pIdx := range patrolIdxs {
			allPatrols = append(allPatrols, patUrgency{idx: pIdx, fuel: remFuel[pIdx]})
		}
		sort.Slice(allPatrols, func(i, j int) bool {
			return allPatrols[i].fuel < allPatrols[j].fuel
		})

		// Ưu tiên các xe <= LowFuelRefuelThreshold
		for _, p := range allPatrols {
			if len(canRefuelToday) >= len(refuelIdxs) {
				break
			}
			if p.fuel <= prof.LowFuelRefuelThreshold {
				canRefuelToday[p.idx] = true
			}
		}

		// Nếu vẫn còn refueler trống chưa có mục tiêu, chọn tiếp các patrol có fuel thấp nhất
		// (miễn là fuel chưa đầy: p.fuel < gs.FuelLimit() hoặc budget >= 60 trên map lớn để tận dụng Refuelers ngay ngày 0)
		for _, p := range allPatrols {
			if len(canRefuelToday) >= len(refuelIdxs) {
				break
			}
			if !canRefuelToday[p.idx] && (p.fuel < gs.FuelLimit() || budget >= 60) {
				canRefuelToday[p.idx] = true
			}
		}
	}

	// pathCache[agentIdx][spotIdx] = (steps, fuel, ok)
	// Chỉ làm mới cache cho agent vừa được gán spot (curPos thay đổi), agent khác giữ nguyên.
	type pathEntry struct {
		steps, fuel int
		ok          bool
	}
	spotAssignedCount := make(map[int]int)
	maxCapPerSpot := 1
	if len(gs.Spots) < 8 {
		maxCapPerSpot = 3
	} else if budget >= 80 && len(patrolIdxs) >= 4 {
		maxCapPerSpot = 2
	}
	isSpotFullyAssigned := func(si int) bool {
		sp := gs.Spots[si]
		if sp.DayStocks <= 0 {
			return true
		}
		capVal := sp.DayStocks
		localCap := maxCapPerSpot
		if budget >= 80 && len(patrolIdxs) >= 5 {
			if sp.DayStocks >= 6 {
				localCap = 4
			} else if sp.DayStocks >= 4 {
				localCap = 3
			}
		}
		if capVal > localCap {
			capVal = localCap
		}
		return spotAssignedCount[si] >= capVal
	}

	pathCache := make(map[int]map[int]pathEntry)
	invalidatePathCache := func(agentIdx int) {
		pathCache[agentIdx] = make(map[int]pathEntry)
		for si := range gs.Spots {
			if isSpotFullyAssigned(si) {
				continue
			}
			p, ok := dc.Patrol.Get(curPos[agentIdx], gs.Spots[si].Pos)
			if ok {
				pathCache[agentIdx][si] = pathEntry{p.Steps, p.Fuel, true}
			} else {
				pathCache[agentIdx][si] = pathEntry{ok: false}
			}
		}
	}
	// Khởi tạo cache lần đầu cho tất cả agents
	for _, a := range patrolIdxs {
		invalidatePathCache(a)
	}

	for {
		type candChoice struct {
			agent int
			spot  int
			steps int
			fuel  int
			val   float64
		}
		var choices []candChoice
		bestVal := -1.0
		haveChoice := false

		// Tính khoảng cách tối thiểu từ vị trí LATEST của từng Patrol tới từng spot (để ưu tiên xe gần nhất đi thu)
		minStepsToSpot := make(map[int]int)
		for si := range gs.Spots {
			if isSpotFullyAssigned(si) {
				continue
			}
			minS := 1 << 30
			for _, a := range patrolIdxs {
				if e, ok := pathCache[a][si]; ok && e.ok && e.steps <= remBudget[a] && (canRefuelToday[a] || e.fuel <= remFuel[a]) && remBudget[a] >= 1 {
					if e.steps < minS {
						minS = e.steps
					}
				}
			}
			minStepsToSpot[si] = minS
		}

		maxSpotsPerAgent := 8
		if budget >= 60 {
			maxSpotsPerAgent = 10
		}
		if budget >= 80 {
			maxSpotsPerAgent = 12
		}

		for _, a := range patrolIdxs {
			if len(assignedToPatrol[a]) >= maxSpotsPerAgent {
				continue
			}
			for si, sp := range gs.Spots {
				if isSpotFullyAssigned(si) {
					continue
				}
				hasSpot := false
				for _, prevSpot := range assignedToPatrol[a] {
					if prevSpot == si {
						hasSpot = true
						break
					}
				}
				if hasSpot {
					continue
				}
				e, cached := pathCache[a][si]
				if !cached || !e.ok {
					continue
				}
				stepCost := e.steps
				if curPos[a] == sp.Pos {
					stepCost = 1
				}
				if stepCost > remBudget[a] || (!canRefuelToday[a] && e.fuel > remFuel[a]) {
					continue
				}

				isNewInMatch := !localBrands[sp.Brand]
				isNewToday := !todayBrands[sp.Brand]
				effectiveValue := 1.0
				if e.steps > 3 {
					effectiveValue -= float64(e.steps-3) * 0.05
					if effectiveValue < 0.2 {
						effectiveValue = 0.2
					}
				}
				prof := GetMapProfile(gs)
				val := effectiveValue - float64(e.steps)*prof.StepFactor

				if minS, ok := minStepsToSpot[si]; ok && minS < (1<<30) && e.steps > minS {
					val -= float64(e.steps-minS) * 0.01
				}

				if isNewInMatch && spotAssignedCount[si] == 0 {
					val += prof.NewBrandBonus
				} else if isNewToday && spotAssignedCount[si] == 0 {
					val += 5.0
				}
				if spotAssignedCount[si] > 0 {
					val -= 4.0 * float64(spotAssignedCount[si])
				}

				choices = append(choices, candChoice{
					agent: a,
					spot:  si,
					steps: e.steps,
					fuel:  e.fuel,
					val:   val,
				})
				if !haveChoice || val > bestVal {
					bestVal = val
					haveChoice = true
				}
			}
		}

		if len(choices) == 0 {
			if maxCapPerSpot < len(patrolIdxs) {
				hasRemBudget := false
				for _, a := range patrolIdxs {
					if remBudget[a] >= 10 {
						hasRemBudget = true
						break
					}
				}
				if hasRemBudget {
					maxCapPerSpot++
					for _, a := range patrolIdxs {
						invalidatePathCache(a)
					}
					continue
				}
			}
			break
		}

		// ------------------------------------------------------------------
		// FIX RCL: dùng biên độ TUYỆT ĐỐI (không chỉ tỉ lệ %) để lọc.
		// Trước đây: `c.val >= 0.7*bestVal` — khi bestVal nhỏ (map ít stock,
		// hoặc sau khi các spot brand-mới trong ngày đã hết), biên độ % co
		// gần về 0 khiến RCL chỉ còn lại đúng 1 phần tử → GRASP mất khả năng
		// random hoá, mọi vòng lặp AssignDay(rng!=nil) ra cùng kết quả với
		// nghiệm tham lam thuần tuý (rng=nil), lãng phí toàn bộ compute budget
		// của vòng lặp anytime trong plan.go.
		//
		// margin = max(rclMinMargin, rclAlpha * |bestVal|) đảm bảo RCL luôn
		// có biên độ tối thiểu rclMinMargin theo GIÁ TRỊ TUYỆT ĐỐI, bất kể
		// bestVal lớn hay nhỏ (kể cả khi bestVal âm).
		// ------------------------------------------------------------------
		margin := math.Max(rclMinMargin, rclAlpha*math.Abs(bestVal))
		threshold := bestVal - margin

		var rcl []candChoice
		for _, c := range choices {
			if c.val >= threshold {
				rcl = append(rcl, c)
			}
		}
		if len(rcl) == 0 {
			rcl = choices
		}

		var chosen candChoice
		if rng != nil && len(rcl) > 0 {
			// Weighted selection: các ứng viên tiệm cận bestVal có xác suất chọn cao hơn đáng kể
			totalW := 0.0
			weights := make([]float64, len(rcl))
			for i, c := range rcl {
				diff := c.val - threshold
				w := math.Exp(diff / margin)
				weights[i] = w
				totalW += w
			}
			r := rng.Float64() * totalW
			acc := 0.0
			chosen = rcl[0]
			for i, c := range rcl {
				acc += weights[i]
				if r <= acc {
					chosen = c
					break
				}
			}
		} else {
			chosen = rcl[0]
			for _, c := range rcl {
				if c.val > chosen.val {
					chosen = c
				}
			}
		}

		bestAgent := chosen.agent
		bestSpot := chosen.spot
		bestSteps := chosen.steps
		bestFuel := chosen.fuel

		if bestAgent < 0 {
			break
		}

		assignedToPatrol[bestAgent] = append(assignedToPatrol[bestAgent], bestSpot)
		spotAssignedCount[bestSpot]++
		curPos[bestAgent] = gs.Spots[bestSpot].Pos
		actualStepCost := bestSteps
		if bestSteps == 0 {
			actualStepCost = 1
		}
		remBudget[bestAgent] -= (actualStepCost * 55) / 100
		remFuel[bestAgent] -= (bestFuel * 55) / 100
		if !localBrands[gs.Spots[bestSpot].Brand] {
			localBrands[gs.Spots[bestSpot].Brand] = true
		}
		if !todayBrands[gs.Spots[bestSpot].Brand] {
			todayBrands[gs.Spots[bestSpot].Brand] = true
		}
		// Chỉ cần làm mới cache cho agent vừa thay đổi vị trí
		invalidatePathCache(bestAgent)
	}
	// Phân công thực tế: gọi dpExact / greedy để tối ưu hóa thứ tự route cho các spot đã gán
	localCollectedBrands := make(map[int]bool)
	for k, v := range collectedBrands {
		localCollectedBrands[k] = v
	}
	spotHarvestCount := make(map[int]int)
	isSpotDepleted := func(si int) bool {
		return spotHarvestCount[si] >= gs.Spots[si].DayStocks
	}

	// Lưu trữ trạng thái sau Phase 2 để dùng cho Phase 3
	type patrolState struct {
		posRoute     []int
		routeIdxs    []int
		cands        []Candidate
		budget_i     int
		usedSteps    int
		usedFuel     int
		fuelRemain   int
		reached      int
		hasStartWait bool
	}
	patrolStates := make(map[int]*patrolState)
	occupiedEndPos := make(map[int]bool)

	for _, i := range patrolIdxs {
		fuelRemain := gs.FuelOf(i)
		// Nhất quán với Phase 1: nếu Refueler đứng cùng ô đầu ngày, dùng full tank để route.
		for _, refIdx := range refuelIdxs {
			if gs.AgentPos[refIdx] == gs.AgentPos[i] {
				fuelRemain = gs.FuelLimit()
				break
			}
		}
		plannedFuel := fuelRemain
		if canRefuelToday[i] {
			plannedFuel = gs.FuelLimit() * 2
		}

		budget_i := budget

		// Chỉ lấy cands từ assignedToPatrol
		var cands []Candidate
		for _, si := range assignedToPatrol[i] {
			sp := gs.Spots[si]
			p, ok := dc.Patrol.Get(gs.AgentPos[i], sp.Pos)
			if !ok {
				continue
			}
			isNew := !localCollectedBrands[sp.Brand]
			stepCost := p.Steps
			if gs.AgentPos[i] == sp.Pos {
				stepCost = 1
			}
			cands = append(cands, Candidate{
				SpotIdx:   si,
				Pos:       sp.Pos,
				Brand:     sp.Brand,
				IsNewType: isNew,
				Value:     1.0,
				Heuristic: 0,                     // Không cần cho Phase 2 (vì đã được global filter)
				StepCost:  stepCost,
				FuelCost:  p.Fuel,
			})
		}

		var routeIdxs []int

		agentTimeout := assignTimeout
		if len(patrolIdxs) > 0 {
			agentTimeout = assignTimeout / time.Duration(len(patrolIdxs))
		}
		agentCtx, agentCancel := context.WithTimeout(ctx, agentTimeout)

		if len(cands) <= 6 && len(cands) <= prof.DpThreshold {
			routeIdxs, _ = dfsExact(agentCtx, i, budget_i, plannedFuel, cands, localCollectedBrands, gs, dc, todayBrands, prof)
		} else {
			// greedyCoverage + twoOpt cực nhanh và tối ưu thứ tự ghé thăm
			routeIdxs = greedyCoverage(agentCtx, i, budget_i, plannedFuel, cands, localCollectedBrands, gs, dc, todayBrands, prof)
			routeIdxs = twoOpt(agentCtx, routeIdxs, cands, gs.AgentPos[i], dc, gs, todayBrands)
		}
		agentCancel()

		// Chuyển SpotIdx → chuỗi pos đầy đủ (gồm cả ô trung gian)
		posRoute, hasStartWait := buildPosRoute(i, routeIdxs, cands, gs, dc)
		usedSteps, usedFuel, reached := SimulateRoute(posRoute, budget_i, fuelRemain, false, gs)

		endPos := posRoute[len(posRoute)-1]
		if reached+1 < len(posRoute) {
			endPos = posRoute[reached]
		}
		if occupiedEndPos[endPos] && len(routeIdxs) >= 2 {
			for tryIdx := len(routeIdxs) - 2; tryIdx >= 0; tryIdx-- {
				altRouteIdxs := make([]int, len(routeIdxs))
				copy(altRouteIdxs, routeIdxs)
				altRouteIdxs[tryIdx], altRouteIdxs[len(altRouteIdxs)-1] = altRouteIdxs[len(altRouteIdxs)-1], altRouteIdxs[tryIdx]
				altPosRoute, altStartWait := buildPosRoute(i, altRouteIdxs, cands, gs, dc)
				altSteps, altFuel, altReached := SimulateRoute(altPosRoute, budget_i, fuelRemain, false, gs)
				altEndPos := altPosRoute[len(altPosRoute)-1]
				if altReached+1 < len(altPosRoute) {
					altEndPos = altPosRoute[altReached]
				}
				if !occupiedEndPos[altEndPos] && altReached >= len(altPosRoute)-1 {
					posRoute = altPosRoute
					hasStartWait = altStartWait
					usedSteps, usedFuel, reached = altSteps, altFuel, altReached
					routeIdxs = altRouteIdxs
					endPos = altEndPos
					break
				}
			}
		}
		occupiedEndPos[endPos] = true

		// Đánh dấu thu hoạch cho các spot THỰC SỰ đi tới
		actuallyVisitedPos := posRoute
		if reached+1 < len(posRoute) {
			actuallyVisitedPos = posRoute[:reached+1]
		}
		for _, idx := range routeIdxs {
			if idx >= 0 && idx < len(cands) {
				sp := cands[idx].SpotIdx
				if posContains(actuallyVisitedPos, gs.Spots[sp].Pos) {
					spotHarvestCount[sp]++
					localCollectedBrands[gs.Spots[sp].Brand] = true
				}
			}
		}

		patrolStates[i] = &patrolState{
			posRoute:     posRoute,
			routeIdxs:    routeIdxs,
			cands:        cands,
			budget_i:     budget_i,
			usedSteps:    usedSteps,
			usedFuel:     usedFuel,
			fuelRemain:   fuelRemain,
			reached:      reached,
			hasStartWait: hasStartWait,
		}
	}

	// --- PHASE 3: OPPORTUNISTIC FULL EXTENSION (Bảo tồn nhiên liệu nghiêm ngặt) ---
	// Chỉ mở rộng lộ trình nếu agent THỰC SỰ ĐẾN TRỌN VẸN được 1 bãi còn tồn kho
	// mà chưa từng thu hoạch hôm nay. Tuyệt đối không di chuyển dở dang, không xóa visited,
	// không đốt cạn xăng vô ích!
	for _, i := range patrolIdxs {
		st := patrolStates[i]
		posRoute := st.posRoute
		remSteps := st.budget_i - st.usedSteps
		remFuel := st.fuelRemain - st.usedFuel

		visited := make(map[int]bool)
		for _, idx := range st.routeIdxs {
			if idx >= 0 && idx < len(st.cands) {
				visited[st.cands[idx].SpotIdx] = true
			}
		}

		minSafeFuel := 10
		if gs.CurrentDay >= len(gs.Setup.DaySteps)-1 {
			minSafeFuel = 1
		}
		for remSteps > 0 && remFuel > minSafeFuel {
			bestTarget := -1
			bestVal := -999999.0
			fromPos := posRoute[len(posRoute)-1]

			for spIdx, sp := range gs.Spots {
				if visited[spIdx] || isSpotDepleted(spIdx) {
					continue
				}
				p, ok := dc.Patrol.Get(fromPos, sp.Pos)
				if !ok || p.Steps <= 0 {
					continue
				}
				// Phải đến ĐỦ trọn vẹn cả steps và fuel
				if p.Steps <= remSteps && p.Fuel <= (remFuel-minSafeFuel) {
					val := 1.0 - float64(p.Steps)*0.01
					if !localCollectedBrands[sp.Brand] {
						val += prof.NewBrandBonus + 10.0
					}
					if occupiedEndPos[sp.Pos] {
						val -= 15.0
					}
					if val > bestVal {
						bestVal = val
						bestTarget = spIdx
					}
				}
			}

			if bestTarget < 0 || bestVal < 0 {
				// Không tới trọn vẹn được bãi nào có kho hoặc bãi duy nhất đã có xe khác đỗ -> Dừng ngay tại đây để bảo tồn xăng và phân tán xe!
				break
			}

			// Mở rộng toàn bộ đường đi đến bestTarget
			p2, ok2 := dc.Patrol.Get(fromPos, gs.Spots[bestTarget].Pos)
			if !ok2 || len(p2.Dirs) == 0 {
				break
			}
			pos := fromPos
			W, H := gs.Width(), gs.Height()
			for _, d := range p2.Dirs {
				nb := neighbor(pos, d, W, H)
				if nb < 0 {
					break
				}
				posRoute = append(posRoute, nb)
				pos = nb
			}
			remSteps -= p2.Steps
			remFuel -= p2.Fuel
			visited[bestTarget] = true
			spotHarvestCount[bestTarget]++
			localCollectedBrands[gs.Spots[bestTarget].Brand] = true
			delete(occupiedEndPos, fromPos)
			occupiedEndPos[gs.Spots[bestTarget].Pos] = true
		}

		patrolRoutes = append(patrolRoutes, posRoute)

		var initialWaitPoints []WaitPoint
		if st.hasStartWait {
			initialWaitPoints = append(initialWaitPoints, WaitPoint{
				Pos:       gs.AgentPos[i],
				WaitSteps: 1,
			})
		}

		plans = append(plans, AgentPlan{
			AgentIdx:   i,
			Route:      posRoute,
			WaitPoints: initialWaitPoints,
		})
	}

	// ------------------------------------------------------------------
	// Pha 4: Phân công Patrol cho Refueler (Clustering) & Kế hoạch Rendezvous
	// ------------------------------------------------------------------
	patrolToRefuel := make(map[int]int)
	if len(refuelIdxs) > 0 {
		// FIX #1: Dự tính MeetPos (điểm hẹn) cho từng Patrol thay vì lấy vị trí xuất phát đầu ngày
		patrolTargetPos := make(map[int]int)
		for _, patIdx := range patrolIdxs {
			piIdx := indexOfPatrol(patrolIdxs, patIdx)
			targetPos := gs.AgentPos[patIdx]
			if piIdx >= 0 && piIdx < len(patrolRoutes) {
				route := patrolRoutes[piIdx]
				rvProbe := planRendezvousFrom(patIdx, gs.AgentPos[patIdx], 1<<30, 0, route, gs, dc, prof)
				if rvProbe != nil {
					targetPos = rvProbe.MeetPos
				}
			}
			patrolTargetPos[patIdx] = targetPos
		}

		// FIX #3: Bài toán gán cân bằng (min-cost load-balanced assignment) cho ≥2 Refueler
		type pairCost struct {
			patIdx int
			refIdx int
			cost   int
		}
		var pairs []pairCost
		for _, patIdx := range patrolIdxs {
			targetPos := patrolTargetPos[patIdx]
			for _, refIdx := range refuelIdxs {
				refPos := gs.AgentPos[refIdx]
				cost := 1 << 28
				if p, ok := dc.Refueler.Get(refPos, targetPos); ok {
					cost = gs.FuelOf(patIdx)*100 + p.Steps
				}
				pairs = append(pairs, pairCost{patIdx: patIdx, refIdx: refIdx, cost: cost})
			}
		}

		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].cost < pairs[j].cost
		})

		refLoad := make(map[int]int)
		maxLoad := (len(patrolIdxs) + len(refuelIdxs) - 1) / max1(len(refuelIdxs))

		for _, pc := range pairs {
			if _, assigned := patrolToRefuel[pc.patIdx]; assigned {
				continue
			}
			if refLoad[pc.refIdx] < maxLoad {
				patrolToRefuel[pc.patIdx] = pc.refIdx
				refLoad[pc.refIdx]++
			}
		}

		// Fallback cho patrol chưa được gán
		for _, patIdx := range patrolIdxs {
			if _, assigned := patrolToRefuel[patIdx]; !assigned {
				bestRef := -1
				minLoad := 1 << 30
				for _, refIdx := range refuelIdxs {
					if refLoad[refIdx] < minLoad {
						minLoad = refLoad[refIdx]
						bestRef = refIdx
					}
				}
				if bestRef == -1 && len(refuelIdxs) > 0 {
					bestRef = refuelIdxs[0]
				}
				if bestRef >= 0 {
					patrolToRefuel[patIdx] = bestRef
					refLoad[bestRef]++
				}
			}
		}
	}

	alreadyRefueledPatrols := make(map[int]bool)

	for _, refIdx := range refuelIdxs {
		refuelRoute := []int{gs.AgentPos[refIdx]}
		var refWaits []WaitPoint
		refBudget := budget
		refCurPos := gs.AgentPos[refIdx]
		W, H := gs.Width(), gs.Height()

		var myPatrolIdxs []int
		for _, patIdx := range patrolIdxs {
			if alreadyRefueledPatrols[patIdx] {
				continue
			}
			if patrolToRefuel[patIdx] == refIdx {
				myPatrolIdxs = append(myPatrolIdxs, patIdx)
			}
		}
		if len(myPatrolIdxs) == 0 {
			for _, patIdx := range patrolIdxs {
				if !alreadyRefueledPatrols[patIdx] {
					myPatrolIdxs = append(myPatrolIdxs, patIdx)
				}
			}
		}

		type rvEntry struct {
			patIdx int
			route  []int
		}
		var urgentPatrols []rvEntry
		for _, patIdx := range myPatrolIdxs {
			piIdx := indexOfPatrol(patrolIdxs, patIdx)
			if piIdx >= 0 && piIdx < len(patrolRoutes) {
				rv := planRendezvousFrom(patIdx, refCurPos, refBudget, 0, patrolRoutes[piIdx], gs, dc, prof)
				if rv != nil {
					urgentPatrols = append(urgentPatrols, rvEntry{patIdx: patIdx, route: patrolRoutes[piIdx]})
				}
			}
		}

		evalSchedule := func(arr []rvEntry) int {
			cost := 0
			curPos := refCurPos
			curBudget := refBudget
			elapsed := 0
			for _, entry := range arr {
				patFuel := gs.FuelOf(entry.patIdx)
				rv := planRendezvousFrom(entry.patIdx, curPos, curBudget, elapsed, entry.route, gs, dc, prof)
				if rv == nil {
					if patFuel <= prof.LowFuelRefuelThreshold {
						cost += 10000000
					} else {
						cost += 1000
					}
					continue
				}
				cost -= (gs.FuelLimit() - patFuel) * 1000
				if rv.MeetStep < rv.PatrolWait {
					cost += (rv.PatrolWait - rv.MeetStep) * 10
				}
				curPos = rv.MeetPos
				elapsed += rv.RefuelStep
				curBudget -= rv.RefuelStep
			}
			cost += elapsed
			return cost
		}

		var bestPerm []rvEntry
		if len(urgentPatrols) <= 6 {
			bestPermCost := 1 << 30
			var permute func(arr []rvEntry, l int)
			permute = func(arr []rvEntry, l int) {
				if l == len(arr) {
					cost := evalSchedule(arr)
					if cost < bestPermCost {
						bestPermCost = cost
						bestPerm = append([]rvEntry(nil), arr...)
					}
					return
				}
				for i := l; i < len(arr); i++ {
					arr[l], arr[i] = arr[i], arr[l]
					permute(arr, l+1)
					arr[l], arr[i] = arr[i], arr[l]
				}
			}
			permute(urgentPatrols, 0)
		} else {
			// FIX #5: Cheapest Insertion khi k > 6 (tối ưu zero-allocation copy)
			bestPerm = make([]rvEntry, 0, len(urgentPatrols))
			for _, entry := range urgentPatrols {
				bestPos := len(bestPerm)
				bestCost := 1 << 30
				for insIdx := 0; insIdx <= len(bestPerm); insIdx++ {
					candPerm := make([]rvEntry, len(bestPerm)+1)
					copy(candPerm[:insIdx], bestPerm[:insIdx])
					candPerm[insIdx] = entry
					copy(candPerm[insIdx+1:], bestPerm[insIdx:])

					cost := evalSchedule(candPerm)
					if cost < bestCost {
						bestCost = cost
						bestPos = insIdx
					}
				}
				candPerm := make([]rvEntry, len(bestPerm)+1)
				copy(candPerm[:bestPos], bestPerm[:bestPos])
				candPerm[bestPos] = entry
				copy(candPerm[bestPos+1:], bestPerm[bestPos:])
				bestPerm = candPerm
			}
		}

		elapsedSteps := 0
		var executedRvs []*RendezvousPlan
		var executedPatIdxs []int

		for _, entry := range bestPerm {
			rv2 := planRendezvousFrom(entry.patIdx, refCurPos, refBudget, elapsedSteps, entry.route, gs, dc, prof)
			if rv2 == nil {
				continue
			}

			p2, ok2 := dc.Refueler.Get(refCurPos, rv2.MeetPos)
			if ok2 && len(p2.Dirs) > 0 {
				pos := refCurPos
				for _, d := range p2.Dirs {
					if refBudget <= 0 {
						break
					}
					nb := neighbor(pos, d, W, H)
					if nb < 0 {
						break
					}
					sc, _, okCell := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
					if !okCell || sc > refBudget {
						break
					}
					refuelRoute = append(refuelRoute, nb)
					refBudget -= sc
					elapsedSteps += sc
					pos = nb
				}
				refCurPos = pos
			} else if (!ok2 || len(p2.Dirs) == 0) && refBudget > 0 && refCurPos != rv2.MeetPos {
				pos := refCurPos
				tRow, tCol := rv2.MeetPos/W, rv2.MeetPos%W
				for refBudget > 0 {
					bestD := -1
					bestDistSq := 999999
					for d := 0; d < 6; d++ {
						nb := neighbor(pos, d, W, H)
						if nb < 0 {
							continue
						}
						sc, _, okCell := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
						if !okCell || sc > refBudget {
							continue
						}
						nbR, nbC := nb/W, nb%W
						dr := nbR - tRow
						dcVal := nbC - tCol
						distSq := dr*dr + dcVal*dcVal
						if distSq < bestDistSq {
							bestDistSq = distSq
							bestD = d
						}
					}
					if bestD < 0 {
						break
					}
					nb := neighbor(pos, bestD, W, H)
					sc, _, _ := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
					refuelRoute = append(refuelRoute, nb)
					refBudget -= sc
					elapsedSteps += sc
					pos = nb
					if pos == rv2.MeetPos {
						break
					}
				}
				refCurPos = pos
			}

			actuallyMet := (refCurPos == rv2.MeetPos)
			if rv2.IsComplete && actuallyMet {
				if rv2.RefuelerWait > 0 {
					refWaits = append(refWaits, WaitPoint{
						Pos:       rv2.MeetPos,
						WaitSteps: rv2.RefuelerWait,
					})
					refBudget -= rv2.RefuelerWait
					elapsedSteps += rv2.RefuelerWait
				}
				executedRvs = append(executedRvs, rv2)
				executedPatIdxs = append(executedPatIdxs, entry.patIdx)
				alreadyRefueledPatrols[entry.patIdx] = true
			}

			if refBudget <= 0 {
				break
			}
		}

		hasShadowTarget := false
		if refBudget > 0 && len(myPatrolIdxs) > 0 {
			bestScore := 1 << 30
			targetPos := -1
			for _, patIdx := range myPatrolIdxs {
				piIdx := indexOfPatrol(patrolIdxs, patIdx)
				if piIdx < 0 || piIdx >= len(patrolRoutes) {
					continue
				}
				route := patrolRoutes[piIdx]
				remFuel := gs.FuelOf(patIdx)
				pos := gs.AgentPos[patIdx]
				if len(route) > 0 {
					for i := 0; i < len(route); i++ {
						_, fc, ok := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
						if !ok {
							break
						}
						remFuel -= fc
						pos = route[i]
					}
				}

				p2, ok2 := dc.Refueler.Get(refCurPos, pos)
				if !ok2 {
					continue
				}

				// Score kết hợp: ưu tiên xe hết xăng.
				// Nhân 3 cho remFuel để Refueler kiên định di chuyển đón xe đói xăng nhất!
				score := remFuel*3 + p2.Steps
				if score < bestScore {
					bestScore = score
					targetPos = pos
				}
			}
			if targetPos >= 0 {
				hasShadowTarget = true
				p2, ok2 := dc.Refueler.Get(refCurPos, targetPos)
				if ok2 && len(p2.Dirs) > 0 {
					pos := refCurPos
					for _, d := range p2.Dirs {
						if refBudget <= 0 {
							break
						}
						nb := neighbor(pos, d, W, H)
						if nb < 0 {
							break
						}
						sc, _, okCell := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
						if !okCell || sc > refBudget {
							break
						}
						refuelRoute = append(refuelRoute, nb)
						refBudget -= sc
						pos = nb
					}
					refCurPos = pos
				}
			}
		}

		// --- PROACTIVE POSITIONING CHO REFUELER ---
		// FIX #4: Chỉ khi KHÔNG có Patrol nào để bám theo (hasShadowTarget == false)
		// mới di chuyển về trọng tâm các spot còn hàng.
		if refBudget > 0 && !hasShadowTarget {
			lowFuelPatrolPos := -1
			minPatrolFuel := 1000
			for _, patIdx := range patrolIdxs {
				f := gs.FuelOf(patIdx)
				if f <= prof.LowFuelRefuelThreshold && f < minPatrolFuel {
					minPatrolFuel = f
					lowFuelPatrolPos = gs.AgentPos[patIdx]
					for _, pl := range plans {
						if pl.AgentIdx == patIdx && len(pl.Route) > 0 {
							lowFuelPatrolPos = pl.Route[len(pl.Route)-1]
							break
						}
					}
				}
			}

			var bestTarget int
			if lowFuelPatrolPos >= 0 {
				bestTarget = lowFuelPatrolPos
			} else {
				sumR, sumC, totalWeight := 0.0, 0.0, 0.0
				for _, sp := range gs.Spots {
					if sp.DayStocks > 0 {
						w := float64(sp.DayStocks)
						if !localCollectedBrands[sp.Brand] {
							w *= 2.0
						}
						sumR += float64(sp.Pos/W) * w
						sumC += float64(sp.Pos%W) * w
						totalWeight += w
					}
				}

				if totalWeight == 0 {
					for _, plan := range plans {
						if gs.IsPatrol(plan.AgentIdx) && len(plan.Route) > 0 {
							finalPos := plan.Route[len(plan.Route)-1]
							sumR += float64(finalPos / W)
							sumC += float64(finalPos % W)
							totalWeight += 1.0
						}
					}
				}

				if totalWeight > 0 {
					centroidR := int(math.Round(sumR / totalWeight))
					centroidC := int(math.Round(sumC / totalWeight))
					centroidPos := centroidR*W + centroidC

					bestTarget = centroidPos
					if gs.Cell(centroidPos) == 3 || centroidPos >= W*H || centroidPos < 0 {
						minDist := 1 << 30
						for pos := 0; pos < W*H; pos++ {
							if gs.Cell(pos) != 3 {
								dr := (pos / W) - centroidR
								dcCol := (pos % W) - centroidC
								dist := dr*dr + dcCol*dcCol
								if dist < minDist {
									minDist = dist
									bestTarget = pos
								}
							}
						}
					}
				} else {
					bestTarget = -1
				}
			}

			if bestTarget >= 0 {
				pCentroid, ok := dc.Refueler.Get(refCurPos, bestTarget)
				if ok && len(pCentroid.Dirs) > 0 {
					pos := refCurPos
					for _, d := range pCentroid.Dirs {
						if refBudget <= 0 {
							break
						}
						nb := neighbor(pos, d, W, H)
						if nb < 0 {
							break
						}
						sc, _, okCell := TerrainCost(gs.Cell(pos), gs.TrafficStatus(pos))
						if !okCell || sc > refBudget {
							break
						}
						refuelRoute = append(refuelRoute, nb)
						refBudget -= sc
						pos = nb
					}
				}
			}
		}

		plans = append(plans, AgentPlan{
			AgentIdx:   refIdx,
			Route:      refuelRoute,
			WaitPoints: refWaits,
		})

		for eIdx, rv2 := range executedRvs {
			patIdx := executedPatIdxs[eIdx]
			remBudget := budget - rv2.RefuelStep
			fullFuel := gs.FuelLimit()
			origPos := gs.AgentPos[patIdx]
			gs.AgentPos[patIdx] = rv2.MeetPos

			var patPlan *AgentPlan
			meetIdx := -1
			for j := range plans {
				if plans[j].AgentIdx == patIdx {
					patPlan = &plans[j]
					for idx, pos := range patPlan.Route {
						if pos == rv2.MeetPos {
							meetIdx = idx
							break
						}
					}
					break
				}
			}

			if patPlan == nil {
				gs.AgentPos[patIdx] = origPos
				continue
			}

			// Nếu Patrol đã có lộ trình cho các bãi tiếp theo sau điểm hẹn meetIdx,
			// GIỮ NGUYÊN lộ trình đó để không phá vỡ các bãi đã được phân công tối ưu từ Phase 1 & 2!
			// Chỉ mở rộng thêm khi xe đã đi hết lộ trình được gán (meetIdx >= len(patPlan.Route)-1)
			// và vẫn còn dư ngân sách bước đi.
			if meetIdx >= 0 && meetIdx < len(patPlan.Route)-1 {
				// Lộ trình tiếp theo đã có sẵn, giữ nguyên để hoàn thành các bãi được gán!
			} else if remBudget > 0 {
				depletedBeforeMeet := make(map[int]bool)
				for si := range gs.Spots {
					if isSpotDepleted(si) {
						depletedBeforeMeet[si] = true
					}
				}
				if meetIdx >= 0 {
					for _, pos := range patPlan.Route[:meetIdx+1] {
						si := gs.SpotIndexAt(pos)
						if si >= 0 {
							depletedBeforeMeet[si] = true
						}
					}
				}

				cands2 := buildCandidates(patIdx, remBudget, fullFuel, localCollectedBrands, depletedBeforeMeet, gs, dc, prof)
				var routeIdxs2 []int
				agentCtx, agentCancel := context.WithTimeout(ctx, 10*time.Millisecond)
				routeIdxs2 = greedyCoverage(agentCtx, patIdx, remBudget, fullFuel, cands2, localCollectedBrands, gs, dc, todayBrands, prof)
				if len(routeIdxs2) > 1 {
					routeIdxs2 = twoOpt(agentCtx, routeIdxs2, cands2, rv2.MeetPos, dc, gs, todayBrands)
				}
				agentCancel()

				for _, idx := range routeIdxs2 {
					if idx >= 0 && idx < len(cands2) {
						sp := cands2[idx].SpotIdx
						spotHarvestCount[sp]++
						localCollectedBrands[gs.Spots[sp].Brand] = true
					}
				}

				if len(routeIdxs2) > 0 {
					posRoute2, _ := buildPosRoute(patIdx, routeIdxs2, cands2, gs, dc)
					if len(posRoute2) > 1 {
						if meetIdx >= 0 {
							patPlan.Route = patPlan.Route[:meetIdx+1]
						}
						patPlan.Route = append(patPlan.Route, posRoute2[1:]...)
					}
				}
			} else {
				if meetIdx >= 0 {
					patPlan.Route = patPlan.Route[:meetIdx+1]
				}
			}

			// Gán WaitPoint bảo toàn startWait nếu có
			var newWps []WaitPoint
			for _, wp := range patPlan.WaitPoints {
				if !wp.IsRefuel {
					newWps = append(newWps, wp)
				}
			}
			newWps = append(newWps, WaitPoint{
				Pos:       rv2.MeetPos,
				WaitSteps: rv2.PatrolWait,
				IsRefuel:  true,
			})
			patPlan.WaitPoints = newWps

			gs.AgentPos[patIdx] = origPos
		}
	}

	// Phase 5: Safety Guard & Validation cho tất cả Patrol plans
	for j, plan := range plans {
		if !gs.IsPatrol(plan.AgentIdx) {
			continue
		}
		hasRefuel := false
		var refuelWp *WaitPoint
		for _, wp := range plan.WaitPoints {
			if wp.IsRefuel {
				hasRefuel = true
				refuelWp = &wp
				break
			}
		}

		startFuel := gs.FuelOf(plan.AgentIdx)

		if !hasRefuel {
			// Không được tiếp xăng: cắt ngắn route theo đúng lượng xăng hiện có
			_, _, reached := SimulateRoute(plan.Route, budget, startFuel, false, gs)
			if reached+1 < len(plan.Route) {
				plans[j].Route = plan.Route[:reached+1]
			}
		} else if refuelWp != nil {
			// Có tiếp xăng: kiểm tra xem chặng trước điểm hẹn có đủ xăng để tới nơi không
			meetPos := refuelWp.Pos
			meetIdx := -1
			for idx, pos := range plan.Route {
				if pos == meetPos {
					meetIdx = idx
					break
				}
			}
			if meetIdx > 0 {
				preRoute := plan.Route[:meetIdx+1]
				preSteps, _, reachedPre := SimulateRoute(preRoute, budget, startFuel, false, gs)
				if reachedPre < meetIdx {
					// Không đủ xăng để đến điểm hẹn -> Hủy cờ refuel và cắt route an toàn trước khi cạn xăng
					var keepWps []WaitPoint
					for _, wp := range plans[j].WaitPoints {
						if !wp.IsRefuel {
							keepWps = append(keepWps, wp)
						}
					}
					plans[j].WaitPoints = keepWps
					plans[j].Route = plan.Route[:reachedPre+1]
				} else if meetIdx < len(plan.Route)-1 {
					remSteps := budget - preSteps - refuelWp.WaitSteps
					_, _, reachedPost := SimulateRoute(plan.Route[meetIdx:], remSteps, gs.FuelLimit(), false, gs)
					if meetIdx+reachedPost+1 < len(plan.Route) {
						plans[j].Route = plan.Route[:meetIdx+reachedPost+1]
					}
				}
			} else if meetIdx == 0 && len(plan.Route) > 1 {
				remSteps := budget - refuelWp.WaitSteps
				_, _, reachedPost := SimulateRoute(plan.Route, remSteps, gs.FuelLimit(), false, gs)
				if reachedPost+1 < len(plan.Route) {
					plans[j].Route = plan.Route[:reachedPost+1]
				}
			}
		}
	}

	// Phase 6: Post-Day Dispersion & Collision Elimination
	// Tránh để 2 xe Patrol đỗ cùng 1 ô qua đêm. Xe nào còn ngân sách bước & xăng
	// sẽ tự động di chuyển tới 1 bãi Udon chưa có xe nào đỗ để xí chỗ ngủ sáng mai ăn free!
	endPosCount := make(map[int]int)
	for _, pl := range plans {
		if gs.IsPatrol(pl.AgentIdx) && len(pl.Route) > 0 {
			ep := pl.Route[len(pl.Route)-1]
			endPosCount[ep]++
		}
	}

	for j := range plans {
		pl := &plans[j]
		if !gs.IsPatrol(pl.AgentIdx) || len(pl.Route) == 0 {
			continue
		}
		ep := pl.Route[len(pl.Route)-1]
		if endPosCount[ep] <= 1 {
			continue // Không bị trùng
		}

		// Tính bước và xăng còn lại của agent này
		hasRefuel := false
		for _, wp := range pl.WaitPoints {
			if wp.IsRefuel {
				hasRefuel = true
				break
			}
		}
		fuelInit := gs.FuelOf(pl.AgentIdx)
		if hasRefuel {
			fuelInit = gs.FuelLimit()
		}
		uSteps, uFuel, _ := SimulateRoute(pl.Route, budget, fuelInit, false, gs)
		remSteps := budget - uSteps
		remFuel := fuelInit - uFuel
		minSafe := 10
		if gs.CurrentDay >= len(gs.Setup.DaySteps)-1 {
			minSafe = 1
		}

		if remSteps <= 0 || remFuel <= minSafe {
			continue
		}

		// Tìm bãi Udon nào chưa có xe Patrol nào đỗ
		bestSpotPos := -1
		bestDist := 1 << 30
		for _, sp := range gs.Spots {
			if endPosCount[sp.Pos] == 0 {
				p, ok := dc.Patrol.Get(ep, sp.Pos)
				if ok && p.Steps <= remSteps && p.Fuel <= (remFuel-minSafe) {
					if p.Steps < bestDist {
						bestDist = p.Steps
						bestSpotPos = sp.Pos
					}
				}
			}
		}

		if bestSpotPos >= 0 {
			p, ok := dc.Patrol.Get(ep, bestSpotPos)
			if ok && len(p.Dirs) > 0 {
				curr := ep
				W, H := gs.Width(), gs.Height()
				for _, d := range p.Dirs {
					nb := neighbor(curr, d, W, H)
					if nb < 0 {
						break
					}
					pl.Route = append(pl.Route, nb)
					curr = nb
				}
				endPosCount[ep]--
				endPosCount[bestSpotPos]++
			}
		}
	}

	return DayAssignment{Plans: plans}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// buildPosRoute mở rộng route từ SpotIdx thành chuỗi POS đầy đủ gồm cả các ô trung gian.
// ⚠️  QUAN TRỌNG: route trả về gồm tất cả ô kề liên tiếp (không chỉ các spot),
// vì EncodeRoute gọi dirTo(from, to) yêu cầu 2 ô PHẢI kề nhau.
// Nếu chỉ trả [agentPos, spot1, spot2], dirTo() sẽ trả -1 → toàn bộ WAIT.
func buildPosRoute(agentIdx int, routeIdxs []int, cands []Candidate, gs *GameState, dc *DayDistCaches) ([]int, bool) {
	hasStartWait := false
	if len(routeIdxs) > 0 && routeIdxs[0] >= 0 && routeIdxs[0] < len(cands) {
		if cands[routeIdxs[0]].Pos == gs.AgentPos[agentIdx] {
			hasStartWait = true
		}
	}
	if len(routeIdxs) == 0 {
		return []int{gs.AgentPos[agentIdx]}, hasStartWait
	}
	W, H := gs.Width(), gs.Height()
	route := []int{gs.AgentPos[agentIdx]}

	for _, idx := range routeIdxs {
		if idx < 0 || idx >= len(cands) {
			continue
		}
		fromPos := route[len(route)-1]
		toPos := cands[idx].Pos

		// Kể trước: nếu agent đã đứng sẵn tại spot, không cần mở rộng — bỏ qua, sang candidate tiếp.
		// Phải check TRƯỚC khi gọi Get vì cache thường không có entry tự vong (src==dst).
		if fromPos == toPos {
			continue
		}

		p, ok := dc.Patrol.Get(fromPos, toPos)
		if !ok || len(p.Dirs) == 0 {
			break // không có đường đi hợp lệ — dừng route
		}
		// Mở rộng từng hướng di chuyển thành vị trí ô tương ứng
		pos := fromPos
		for _, d := range p.Dirs {
			nb := neighbor(pos, d, W, H)
			if nb < 0 {
				goto done // out of bounds — không nên xảy ra với Dijkstra đúng
			}
			route = append(route, nb)
			pos = nb
		}
	}
done:
	return route, hasStartWait
}

func routeValue(idxs []int, cands []Candidate, collectedBrands map[int]bool, agentPos int, dc *DayDistCaches, gs *GameState, todayBrands map[int]bool, prof *MapProfile) float64 {
	brands := copyBrands(collectedBrands)
	tBrands := copyBrands(todayBrands)
	val := 0.0
	cur := agentPos
	steps := 0
	for _, i := range idxs {
		if i < 0 || i >= len(cands) {
			continue
		}
		c := cands[i]
		p, ok := dc.Patrol.Get(cur, c.Pos)
		if ok {
			steps += p.Steps
			if cur == c.Pos {
				steps += 1 // 1 step chờ thu hoạch bãi xuất phát
			}
		}

		spotVal := 0.0
		isNewInMatch := !brands[c.Brand]
		isNewToday := !tBrands[c.Brand]

		if isNewInMatch {
			spotVal += prof.NewBrandBonus + 15.0
			brands[c.Brand] = true
			tBrands[c.Brand] = true
		} else if isNewToday {
			spotVal += 5.0
			tBrands[c.Brand] = true
		}
		spotVal += float64(c.Value)
		val += spotVal - float64(steps)*0.005
		cur = c.Pos
	}
	return val
}

func countRouteSteps(idxs []int, cands []Candidate, agentIdx int, agentPos int, dc *DayDistCaches, gs *GameState) int {
	if len(idxs) == 0 {
		return 0
	}
	total := 0
	cur := agentPos
	for _, i := range idxs {
		if i < 0 || i >= len(cands) {
			break
		}
		p, ok := dc.Patrol.Get(cur, cands[i].Pos)
		if !ok {
			return 1 << 30
		}
		total += p.Steps
		if cur == cands[i].Pos {
			total += 1
		}
		cur = cands[i].Pos
	}
	return total
}

func routeCostSteps(idxs []int, cands []Candidate, agentPos int, dc *DayDistCaches) int {
	total := 0
	cur := agentPos
	for _, i := range idxs {
		if i < 0 || i >= len(cands) {
			break
		}
		p, ok := dc.Patrol.Get(cur, cands[i].Pos)
		if !ok {
			return 1 << 30
		}
		total += p.Steps
		if cur == cands[i].Pos {
			total += 1
		}
		cur = cands[i].Pos
	}
	return total
}

func reverseSegment(route []int, i, j int) []int {
	r := make([]int, len(route))
	copy(r, route)
	for l, ri := i, j; l < ri; l, ri = l+1, ri-1 {
		r[l], r[ri] = r[ri], r[l]
	}
	return r
}

func copyBrands(m map[int]bool) map[int]bool {
	c := make(map[int]bool, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func max1(a int) int {
	if a < 1 {
		return 1
	}
	return a
}

// indexOfPatrol tìm vị trí của patIdx trong slice patrolIdxs.
func indexOfPatrol(patrolIdxs []int, patIdx int) int {
	for i, v := range patrolIdxs {
		if v == patIdx {
			return i
		}
	}
	return -1
}

func posContains(arr []int, pos int) bool {
	for _, p := range arr {
		if p == pos {
			return true
		}
	}
	return false
}