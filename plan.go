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

	var bestActions [][]int
	var bestNewMatchTypes int
	var bestNewTodayTypes int
	var bestStocks int
	var bestSteps int = -1
	iterations := 0

	// GRASP Loop
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	bestEndDist := 1 << 30
	// Khởi tạo assignment đầu tiên (Greedy - rng = nil) để có baseline
	assignment := AssignDay(searchCtx, budget, bt.Collected, gs, dc, nil)
	if len(assignment.Plans) > 0 {
		bestActions = EncodePlan(assignment.Plans, day, gs)
		bestNewMatchTypes, bestNewTodayTypes, bestStocks = scoreActions(bestActions, gs, bt.Collected, day)
		bestSteps = CountBudgetUsedAll(bestActions, gs)
		bestEndDist = calcEndDistToCentroid(bestActions, gs)
	}

	noImproveCount := 0
	maxNoImprove := profile.MaxNoImprove
	maxIterations := profile.MaxGRASPIterations

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
				goto FINISH_SEARCH
			}
			continue
		}
		
		candActions := EncodePlan(candAssignment.Plans, day, gs)
		newMatchTypes, newTodayTypes, stocks := scoreActions(candActions, gs, bt.Collected, day)
		steps := CountBudgetUsedAll(candActions, gs)
		endDist := calcEndDistToCentroid(candActions, gs)
		
		if newMatchTypes > bestNewMatchTypes || 
		  (newMatchTypes == bestNewMatchTypes && newTodayTypes > bestNewTodayTypes) ||
		  (newMatchTypes == bestNewMatchTypes && newTodayTypes == bestNewTodayTypes && stocks > bestStocks) || 
		  (newMatchTypes == bestNewMatchTypes && newTodayTypes == bestNewTodayTypes && stocks == bestStocks && (bestSteps == 0 || steps < bestSteps)) ||
		  (newMatchTypes == bestNewMatchTypes && newTodayTypes == bestNewTodayTypes && stocks == bestStocks && steps == bestSteps && endDist < bestEndDist) {
			bestNewMatchTypes = newMatchTypes
			bestNewTodayTypes = newTodayTypes
			bestStocks = stocks
			bestSteps = steps
			bestEndDist = endDist
			bestActions = candActions
			noImproveCount = 0
			log.Printf("[DAY %d] iter=%d NEW BEST: matchTypes=%d todayTypes=%d stocks=%d steps=%d endDist=%d", day, iterations, newMatchTypes, newTodayTypes, stocks, steps, endDist)
		} else {
			noImproveCount++
			if noImproveCount >= maxNoImprove {
				goto FINISH_SEARCH
			}
		}
	}

FINISH_SEARCH:
	log.Printf("[DAY %d] Search completed in %d iterations. Best: matchTypes=%d, todayTypes=%d, stocks=%d, steps=%d", day, iterations, bestNewMatchTypes, bestNewTodayTypes, bestStocks, bestSteps)

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

// calcEndDistToCentroid tính tổng khoảng cách Chebyshev từ điểm kết thúc cuối ngày của tất cả Patrol agents tới trọng tâm các bãi Udon.
func calcEndDistToCentroid(actions [][]int, gs *GameState) int {
	W, H := gs.Width(), gs.Height()
	sumR, sumC, totalW := 0.0, 0.0, 0.0
	for _, sp := range gs.Spots {
		w := float64(max1(sp.DayStocks))
		sumR += float64(sp.Pos/W) * w
		sumC += float64(sp.Pos%W) * w
		totalW += w
	}
	if totalW == 0 {
		return 0
	}
	centroidR := int(math.Round(sumR / totalW))
	centroidC := int(math.Round(sumC / totalW))

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
		r := pos / W
		c := pos % W
		dr := math.Abs(float64(r - centroidR))
		dcCol := math.Abs(float64(c - centroidC))
		dist := int(math.Max(dr, dcCol))
		totalDist += dist
	}
	return totalDist
}