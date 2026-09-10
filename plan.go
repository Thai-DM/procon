// plan.go — Greedy Maximum Coverage + Anytime Submit Loop.
// Cung cấp hàm RunDay: submit sớm 1 nghiệm an toàn, cải thiện và submit lại
// trong ngân sách thời gian phản hồi còn lại.
package main

import (
	"context"
	"log"
	"math"
	"math/rand"
	"time"
)

const (
	// Thời gian dành cho 1 vòng lặp anytime (tính từ đầu ngày).
	// Phải nhỏ hơn deadline server — cần tune theo trận thật.
	anytimeDeadline = 4 * time.Second

	// Thời gian tối thiểu còn lại để gửi lại (tránh gửi trễ).
	minTimeForResubmit = 200 * time.Millisecond
)

// ---------------------------------------------------------------------------
// collectedBrands — theo dõi brand đã thu toàn trận (dùng trong plan.go).
// ---------------------------------------------------------------------------

// BrandTracker theo dõi brand đã thu toàn trận và lũy kế theo ngày.
type BrandTracker struct {
	Collected   map[int]bool // brand → đã thu ≥ 1 lần toàn trận
	DayNewTypes []int        // số loại mới thu được mỗi ngày (index = day)
}

func NewBrandTracker() *BrandTracker {
	return &BrandTracker{Collected: make(map[int]bool)}
}

// RecordBrand ghi nhận brand thu được trong ngày day.
// Trả true nếu là loại MỚI (chưa từng thu trước đó).
func (bt *BrandTracker) RecordBrand(brand, day int) bool {
	if bt.Collected[brand] {
		return false
	}
	bt.Collected[brand] = true
	for len(bt.DayNewTypes) <= day {
		bt.DayNewTypes = append(bt.DayNewTypes, 0)
	}
	bt.DayNewTypes[day]++
	return true
}

// ---------------------------------------------------------------------------
// RunDay — entry point chính mỗi ngày.
// 1. Build Dijkstra cache (song song Patrol+Refueler).
// 2. Submit ngay nghiệm safe (tất cả đứng yên) nếu cần.
// 3. Chạy AssignDay → EncodePlan → PostActions (submit lần 1).
// 4. Vòng lặp anytime: cải thiện → submit lại nếu còn thời gian.
// ---------------------------------------------------------------------------

// RunDayWithTransport chạy kế hoạch cho 1 ngày với transport bất kỳ (HTTP hoặc WS).
func RunDayWithTransport(
	ctx context.Context,
	st *DayState,
	gs *GameState,
	bt *BrandTracker,
	tr GameTransport,
) [][]int {
	day := st.Day
	budget := gs.DayStepsForDay(day)
	
	profile := GetMapProfile(gs)
	startTime := time.Now()
	deadline := time.Now().Add(profile.DayTimeout)
	searchCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	log.Printf("[DAY %d] [%s] Dynamic timeout=%v (budget=%d steps)...", day, profile.Name, profile.DayTimeout, budget)
	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)
	log.Printf("[DAY %d] Cache built (%d sources).", day, len(srcs))

	totalPossibleStocks := 0
	mapBrands := make(map[int]bool)
	for _, sp := range gs.Spots {
		totalPossibleStocks += sp.MaxStocks
		mapBrands[sp.Brand] = true
	}
	maxPossibleTodayTypes := len(mapBrands)

	remMatchBrands := 0
	for b := range mapBrands {
		if !bt.Collected[b] {
			remMatchBrands++
		}
	}

	var bestActions [][]int
	var bestNewMatchTypes int
	var bestNewTodayTypes int
	var bestStocks int
	var bestSteps int = -1
	iterations := 0

	// GRASP Loop
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	isFinalDay := (day >= len(gs.Setup.DaySteps)-1)
	calcEffectiveStocks := func(stks int, actions [][]int) int {
		if isFinalDay {
			return stks
		}
		col := countPatrolCollisions(actions, gs)
		return stks - col*2
	}

	bestEffectiveStocks := -1
	bestEndDist := 1 << 30
	// Khởi tạo assignment đầu tiên (Greedy - rng = nil) để có baseline
	assignment := AssignDay(searchCtx, budget, bt.Collected, gs, dc, nil)
	if len(assignment.Plans) > 0 {
		bestActions = EncodePlan(assignment.Plans, day, gs)
		bestNewMatchTypes, bestNewTodayTypes, bestStocks = scoreActions(bestActions, gs, bt.Collected, day)
		bestEffectiveStocks = calcEffectiveStocks(bestStocks, bestActions)
		bestSteps = CountBudgetUsedAll(bestActions, gs)
		bestEndDist = calcEndDistToCentroid(bestActions, gs)
	}

	noImproveCount := 0
	maxNoImprove := profile.MaxNoImprove
	if day == 0 {
		maxNoImprove = int(float64(maxNoImprove) * 1.5) // Cho phép tìm kiếm sâu hơn ở ngày đầu
	}
	maxIterations := profile.MaxGRASPIterations
	graceMaxCount := 0

	for {
		select {
		case <-searchCtx.Done():
			goto FINISH_SEARCH
		default:
		}
		
		iterations++
		if iterations >= maxIterations {
			goto FINISH_SEARCH
		}
		candAssignment := AssignDay(searchCtx, budget, bt.Collected, gs, dc, rng)
		if len(candAssignment.Plans) == 0 {
			noImproveCount++
			if noImproveCount >= maxNoImprove {
				log.Printf("[DAY %d] ⏹️ Converged after %d iters with no improvement (total %d iters) -> early exit!", day, maxNoImprove, iterations)
				goto FINISH_SEARCH
			}
			continue
		}
		
		candActions := EncodePlan(candAssignment.Plans, day, gs)
		newMatchTypes, newTodayTypes, stocks := scoreActions(candActions, gs, bt.Collected, day)
		effectiveStocks := calcEffectiveStocks(stocks, candActions)
		steps := CountBudgetUsedAll(candActions, gs)
		endDist := calcEndDistToCentroid(candActions, gs)
		
		isBetter := false
		if newMatchTypes > bestNewMatchTypes {
			isBetter = true
		} else if newMatchTypes == bestNewMatchTypes {
			if newTodayTypes > bestNewTodayTypes {
				isBetter = true
			} else if newTodayTypes == bestNewTodayTypes {
				if effectiveStocks > bestEffectiveStocks {
					isBetter = true
				} else if effectiveStocks == bestEffectiveStocks {
					if stocks > bestStocks {
						isBetter = true
					} else if stocks == bestStocks {
						if endDist < bestEndDist {
							isBetter = true
						} else if endDist == bestEndDist && (bestSteps == 0 || steps < bestSteps) {
							isBetter = true
						}
					}
				}
			}
		}

		if isBetter {
			bestNewMatchTypes = newMatchTypes
			bestNewTodayTypes = newTodayTypes
			bestStocks = stocks
			bestEffectiveStocks = effectiveStocks
			bestSteps = steps
			bestEndDist = endDist
			bestActions = candActions
			noImproveCount = 0
			log.Printf("[DAY %d] iter=%d NEW BEST: matchTypes=%d todayTypes=%d stocks=%d (eff=%d) steps=%d endDist=%d", day, iterations, newMatchTypes, newTodayTypes, stocks, effectiveStocks, steps, endDist)
		} else {
			noImproveCount++
			if noImproveCount >= maxNoImprove {
				log.Printf("[DAY %d] ⏹️ Converged after %d iters with no improvement (total %d iters) -> early exit!", day, maxNoImprove, iterations)
				goto FINISH_SEARCH
			}
		}

		// Kiểm tra đạt trần lý thuyết tuyệt đối: toàn bộ Udon và tất cả brand trên map đều đã thu hoạch
		if bestStocks >= totalPossibleStocks && bestNewTodayTypes >= maxPossibleTodayTypes && bestNewMatchTypes >= remMatchBrands {
			graceMaxCount++
			if graceMaxCount >= 20 {
				log.Printf("[DAY %d] 🎯 THEORETICAL MAXIMUM REACHED (stocks=%d/%d, todayTypes=%d/%d, matchTypes=%d/%d) -> early exit at iter=%d!",
					day, bestStocks, totalPossibleStocks, bestNewTodayTypes, maxPossibleTodayTypes, bestNewMatchTypes, remMatchBrands, iterations)
				goto FINISH_SEARCH
			}
		}
	}

FINISH_SEARCH:
	log.Printf("[DAY %d] Search completed in %d iterations. Best: matchTypes=%d, todayTypes=%d, stocks=%d/%d, steps=%d",
		day, iterations, bestNewMatchTypes, bestNewTodayTypes, bestStocks, totalPossibleStocks, bestSteps)

	// Nếu không tính được plan, dùng safe (tất cả đứng yên)
	if len(bestActions) == 0 {
		bestActions = makeSafeActions(len(st.Agents), budget)
		if res, err := tr.PostActions(ctx, bestActions); err != nil {
			log.Printf("[DAY %d] safe submit error: %v", day, err)
		} else if res != nil && !res.Ok && res.Reason != "" {
			log.Printf("[DAY %d] ⚠️ SAFE ACTIONS BỊ TỪ CHỐI: %s", day, res.Reason)
		} else {
			updateBrandTracker(bt, bestActions, day, gs)
		}
		return bestActions
	}

	bestActions = SanitizeActionsFuelSafe(bestActions, budget, gs)

	res, err := tr.PostActions(ctx, bestActions)
	if err != nil {
		log.Printf("[DAY %d] submit failed: %v", day, err)
	} else if res != nil && !res.Ok && res.Reason != "" {
		log.Printf("[DAY %d] ⚠️ ACTIONS BỊ TỪ CHỐI bởi server: %s — CẢ NGÀY không thực thi!", day, res.Reason)
	} else {
		log.Printf("[DAY %d] final plan submitted OK (%dms taken).",
			day, time.Since(startTime).Milliseconds())
		updateBrandTracker(bt, bestActions, day, gs)
	}
	log.Printf("[DAY %d] DEBUG brands all-time=%d/%d | dayNewTypes(all-time-basis)=%v",
		day, len(bt.Collected), len(gs.Spots), bt.DayNewTypes)

	return bestActions
}

// RunDay — backward compat wrapper dùng *NetworkClient.
func RunDay(ctx context.Context, st *DayState, gs *GameState, bt *BrandTracker, nc *NetworkClient) [][]int {
	return RunDayWithTransport(ctx, st, gs, bt, nc)
}



// ---------------------------------------------------------------------------
// Helpers nội bộ
// ---------------------------------------------------------------------------

// makeSafeActions tạo nghiệm an toàn: tất cả agent đứng yên cả ngày.
func makeSafeActions(n, budget int) [][]int {
	out := make([][]int, n)
	for i := range out {
		out[i] = []int{-budget}
	}
	return out
}

// actionsImprove so sánh 2 bộ actions theo tiêu chí thắng HEXUDON.
// Ưu tiên: số loại mới > tổng stocks thu. Trả true nếu candidate tốt hơn current.
func actionsImprove(candidate, current [][]int, gs *GameState, collectedBrands map[int]bool, day int) bool {
	cMatchT, cTodayT, cStocks := scoreActions(candidate, gs, collectedBrands, day)
	curMatchT, curTodayT, curStocks := scoreActions(current, gs, collectedBrands, day)

	if cMatchT != curMatchT {
		return cMatchT > curMatchT
	}
	if cTodayT != curTodayT {
		return cTodayT > curTodayT
	}
	return cStocks > curStocks
}

// scoreActions ước lượng (số loại mới trận, số loại mới ngày, tổng stocks) từ action sequence.
// Mô phỏng nhanh bằng SimulateRoute — không chính xác 100% nhưng đủ để so sánh.
func scoreActions(actions [][]int, gs *GameState, collectedBrands map[int]bool, day int) (newMatchTypes, newTodayTypes, stocks int) {
	matchBrands := copyBrands(collectedBrands)
	todayBrands := make(map[int]bool)
	remSpotStocks := make(map[int]int, len(gs.Spots))
	for i, sp := range gs.Spots {
		remSpotStocks[i] = sp.DayStocks
	}
	agentVisited := make(map[int]map[int]bool, len(gs.AgentPos))

	for i, seq := range actions {
		if i >= len(gs.AgentPos) || !gs.IsPatrol(i) {
			continue
		}
		agentVisited[i] = make(map[int]bool)
		pos := gs.AgentPos[i]
		W, H := gs.Width(), gs.Height()

		siStart := gs.SpotIndexAt(pos)
		if siStart >= 0 && (len(seq) == 0 || seq[0] < 0) {
			sp := gs.Spots[siStart]
			if !agentVisited[i][siStart] && remSpotStocks[siStart] > 0 {
				agentVisited[i][siStart] = true
				remSpotStocks[siStart]--
				if !matchBrands[sp.Brand] {
					newMatchTypes++
					matchBrands[sp.Brand] = true
				}
				if !todayBrands[sp.Brand] {
					newTodayTypes++
					todayBrands[sp.Brand] = true
				}
				stocks += 1
			}
		}

		for _, a := range seq {
			if a >= 0 && a <= 5 {
				nb := neighbor(pos, a, W, H)
				if nb < 0 {
					break
				}
				pos = nb
				si := gs.SpotIndexAt(pos)
				if si >= 0 {
					sp := gs.Spots[si]
					if !agentVisited[i][si] && remSpotStocks[si] > 0 {
						agentVisited[i][si] = true
						remSpotStocks[si]--
						if !matchBrands[sp.Brand] {
							newMatchTypes++
							matchBrands[sp.Brand] = true
						}
						if !todayBrands[sp.Brand] {
							newTodayTypes++
							todayBrands[sp.Brand] = true
						}
						stocks += 1
					}
				}
			}
		}
	}
	return
}

// updateBrandTracker cập nhật BrandTracker từ actions đã submit.
func updateBrandTracker(bt *BrandTracker, actions [][]int, day int, gs *GameState) {
	W, H := gs.Width(), gs.Height()
	for i, seq := range actions {
		if i >= len(gs.AgentPos) || !gs.IsPatrol(i) {
			continue
		}
		pos := gs.AgentPos[i]
		// Thu hoạch tại vị trí xuất phát nếu có lệnh WAIT ở bước đầu
		siStart := gs.SpotIndexAt(pos)
		if siStart >= 0 && (len(seq) == 0 || seq[0] < 0) {
			bt.RecordBrand(gs.Spots[siStart].Brand, day)
		}
		// Ở hàm updateBrandTracker cũng không cần check fuel (tương tự scoreActions)
		for _, a := range seq {
			if a >= 0 && a <= 5 {
				nb := neighbor(pos, a, W, H)
				if nb < 0 {
					break
				}
				pos = nb
				si := gs.SpotIndexAt(pos)
				if si >= 0 {
					bt.RecordBrand(gs.Spots[si].Brand, day)
				}
			}
		}
	}
}

// calcEndDistToCentroid đánh giá chất lượng vị trí kết thúc ngày của cả đội:
// 1. Khoảng cách Chebyshev từ điểm đỗ của mỗi Patrol tới bãi Udon gần nhất (càng gần bãi càng tốt).
// 2. Phạt nặng nếu 2 xe Patrol đỗ trùng cùng 1 bãi (để các xe tỏa ra đỗ ở các bãi khác nhau, sáng hôm sau ăn free).
func calcEndDistToCentroid(actions [][]int, gs *GameState) int {
	W, H := gs.Width(), gs.Height()
	endSpots := make(map[int]int)

	totalDist := 0
	for i, seq := range actions {
		if i >= len(gs.AgentPos) || !gs.IsPatrol(i) {
			continue
		}
		pos := gs.AgentPos[i]
		for _, a := range seq {
			if a >= 0 && a <= 5 {
				nb := neighbor(pos, a, W, H)
				if nb >= 0 {
					pos = nb
				}
			}
		}
		endSpots[pos]++

		// Tìm khoảng cách tới bãi Udon gần nhất
		minD := 1 << 30
		r := pos / W
		c := pos % W
		for _, sp := range gs.Spots {
			spR := sp.Pos / W
			spC := sp.Pos % W
			dr := math.Abs(float64(r - spR))
			dcCol := math.Abs(float64(c - spC))
			dist := int(math.Max(dr, dcCol))
			if dist < minD {
				minD = dist
			}
		}
		totalDist += minD
	}

	// Phạt nếu có từ 2 xe trở lên đỗ trùng ô cuối ngày (phạt nặng để các xe tản ra bãi khác nhau)
	for _, count := range endSpots {
		if count > 1 {
			totalDist += (count - 1) * 150
		}
	}

	return totalDist
}

// countPatrolCollisions đếm số lượng xe Patrol bị đỗ trùng ô cuối ngày với xe Patrol khác.
func countPatrolCollisions(actions [][]int, gs *GameState) int {
	W, H := gs.Width(), gs.Height()
	endCounts := make(map[int]int)
	for i, seq := range actions {
		if i >= len(gs.AgentPos) || !gs.IsPatrol(i) {
			continue
		}
		pos := gs.AgentPos[i]
		for _, a := range seq {
			if a >= 0 && a <= 5 {
				nb := neighbor(pos, a, W, H)
				if nb >= 0 {
					pos = nb
				}
			}
		}
		endCounts[pos]++
	}
	collisions := 0
	for _, c := range endCounts {
		if c > 1 {
			collisions += (c - 1)
		}
	}
	return collisions
}

// SanitizeActionsFuelSafe kiểm tra và chuẩn hoá toàn bộ hành động của cả đội trước khi submit.
// Mô phỏng chính xác tiến trình từng bước (t = 1..budget) của tất cả agent đồng bộ:
// - Nạp đầy bình (FuelLimit) cho Patrol nếu Patrol và Refueler ở cùng ô tại bất kỳ thời điểm nào.
// - Chặn TUYỆT ĐỐI lỗi E_NO_FUEL, E_STEP_OVERFLOW, E_POND, E_NOT_ADJACENT:
//   Nếu 1 Patrol sắp thực hiện bước đi không hợp lệ hoặc thiếu fuel,
//   cắt an toàn tại bước đó và đệm lệnh WAIT (-remainingSteps) cho tới hết ngày.
// - Đảm bảo mọi agent tiêu thụ đúng số bước budget và submission luôn 100% hợp lệ!
func SanitizeActionsFuelSafe(actions [][]int, budget int, gs *GameState) [][]int {
	n := len(gs.AgentPos)
	W, H := gs.Width(), gs.Height()

	sanitized := make([][]int, n)

	// Trạng thái mô phỏng cho từng agent
	pos := make([]int, n)
	fuel := make([]int, n)
	isRefueler := make([]bool, n)
	cmdIdx := make([]int, n)
	busySteps := make([]int, n)
	nextPos := make([]int, n)
	isMoving := make([]bool, n)
	stopped := make([]bool, n)

	for i := 0; i < n; i++ {
		pos[i] = gs.AgentPos[i]
		fuel[i] = gs.FuelOf(i)
		isRefueler[i] = !gs.IsPatrol(i) && gs.AgentFuel[i] == 0
		nextPos[i] = pos[i]
		if i < len(actions) {
			sanitized[i] = make([]int, 0, len(actions[i]))
		} else {
			sanitized[i] = []int{-budget}
			stopped[i] = true
		}
	}

	// Nạp xăng tại ô xuất phát (bước 0) nếu Patrol trùng ô Refueler
	for r := 0; r < n; r++ {
		if isRefueler[r] {
			for p := 0; p < n; p++ {
				if !isRefueler[p] && pos[p] == pos[r] {
					fuel[p] = gs.FuelLimit()
				}
			}
		}
	}

	for t := 1; t <= budget; t++ {
		stepsRemainingToday := budget - (t - 1)

		// 1. Kích hoạt lệnh tiếp theo cho các agent đã hoàn thành lệnh trước
		for i := 0; i < n; i++ {
			if stopped[i] {
				continue
			}

			for busySteps[i] == 0 {
				if i >= len(actions) || cmdIdx[i] >= len(actions[i]) {
					// Hết lệnh: đệm WAIT đến hết ngày
					if stepsRemainingToday > 0 {
						sanitized[i] = append(sanitized[i], -stepsRemainingToday)
						busySteps[i] = stepsRemainingToday
						nextPos[i] = pos[i]
						isMoving[i] = false
					}
					stopped[i] = true
					break
				}

				cmd := actions[i][cmdIdx[i]]
				cmdIdx[i]++

				if cmd < 0 {
					// Lệnh WAIT
					w := -cmd
					if w > stepsRemainingToday {
						w = stepsRemainingToday
					}
					if w > 0 {
						sanitized[i] = append(sanitized[i], -w)
						busySteps[i] = w
						nextPos[i] = pos[i]
						isMoving[i] = false
					}
					break
				} else if cmd >= 0 && cmd <= 5 {
					// Lệnh di chuyển
					nb := neighbor(pos[i], cmd, W, H)
					sc, fc, canMove := TerrainCost(gs.Cell(pos[i]), gs.TrafficStatus(pos[i]))
					if isRefueler[i] {
						fc = 0
					}

					legal := (nb >= 0) && canMove && (gs.Cell(nb) != 3) && (sc <= stepsRemainingToday) && (fuel[i] >= fc)

					if !legal {
						// Bước đi không an toàn / không đủ xăng -> Chặn ngay và đệm WAIT an toàn
						if stepsRemainingToday > 0 {
							sanitized[i] = append(sanitized[i], -stepsRemainingToday)
							busySteps[i] = stepsRemainingToday
							nextPos[i] = pos[i]
							isMoving[i] = false
						}
						stopped[i] = true
						log.Printf("[SAFETY GUARD] 🛡️ Xe %d dừng an toàn tại bước %d (fuel=%d, cần=%d, bước còn=%d) -> Tránh E_NO_FUEL/lỗi server!",
							i, t, fuel[i], fc, stepsRemainingToday)
						break
					}

					// Trừ fuel và bắt đầu di chuyển
					fuel[i] -= fc
					busySteps[i] = sc
					nextPos[i] = nb
					isMoving[i] = true
					sanitized[i] = append(sanitized[i], cmd)
					break
				}
			}
		}

		// 2. Tiến thời gian 1 bước
		for i := 0; i < n; i++ {
			if busySteps[i] > 0 {
				busySteps[i]--
				if busySteps[i] == 0 {
					pos[i] = nextPos[i]
					isMoving[i] = false
				}
			}
		}

		// 3. Cuối bước t: Kiểm tra tiếp xăng
		// Patrol được nạp đầy bình nếu tại thời điểm này, Refueler và Patrol cùng ở trên 1 ô
		for r := 0; r < n; r++ {
			if !isRefueler[r] || isMoving[r] {
				continue
			}
			refPos := pos[r]
			for p := 0; p < n; p++ {
				if isRefueler[p] || isMoving[p] {
					continue
				}
				if pos[p] == refPos {
					fuel[p] = gs.FuelLimit()
				}
			}
		}
	}

	// 4. Chuẩn hóa & đệm đảm bảo tổng bước chính xác bằng budget
	for i := 0; i < n; i++ {
		curP := gs.AgentPos[i]
		totalUsed := 0
		var validSeq []int
		for _, a := range sanitized[i] {
			if a < 0 {
				w := -a
				if totalUsed+w > budget {
					w = budget - totalUsed
				}
				if w > 0 {
					validSeq = append(validSeq, -w)
					totalUsed += w
				}
			} else if a >= 0 && a <= 5 {
				sc, _, _ := TerrainCost(gs.Cell(curP), gs.TrafficStatus(curP))
				if totalUsed+sc <= budget {
					validSeq = append(validSeq, a)
					totalUsed += sc
					curP = neighbor(curP, a, W, H)
				}
			}
		}
		if totalUsed < budget {
			validSeq = append(validSeq, -(budget - totalUsed))
		}
		if len(validSeq) == 0 {
			validSeq = []int{-budget}
		}
		sanitized[i] = validSeq
	}

	return sanitized
}