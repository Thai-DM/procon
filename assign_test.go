package main

import (
	"context"
	"math/rand"
	"testing"
)

func TestAssignKinds(t *testing.T) {
	// TH1: Map 8x8, 4 agents, 4 days, fuel 60 -> 100% patrols
	s1 := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{30, 30, 30, 30},
		FuelLimits: 60,
	}
	s1.Map.Width = 8
	s1.Map.Height = 8

	kinds1 := assignKinds(s1)
	for i, k := range kinds1 {
		if k != 0 {
			t.Errorf("Expected agent %d to be Patrol (0), got %d", i, k)
		}
	}

	// TH2: Map 8x8, 4 agents, 10 days, fuel 60 -> 1 Refueler
	s2 := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   make([]int, 10),
		FuelLimits: 60,
	}
	for i := range s2.DaySteps {
		s2.DaySteps[i] = 30
	}
	s2.Map.Width = 8
	s2.Map.Height = 8

	kinds2 := assignKinds(s2)
	refCount := 0
	for _, k := range kinds2 {
		if k == 1 {
			refCount++
		}
	}
	if refCount != 1 {
		t.Errorf("Expected 1 Refueler for 10-day 8x8 match, got %d", refCount)
	}

	// TH3: Map 8x8, 4 agents, 5 days, fuel 60 -> 1 Refueler (60 < 5*13=65)
	s3 := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{30, 30, 30, 30, 30},
		FuelLimits: 60,
	}
	s3.Map.Width = 8
	s3.Map.Height = 8
	kinds3 := assignKinds(s3)
	refCount3 := 0
	for _, k := range kinds3 {
		if k == 1 {
			refCount3++
		}
	}
	if refCount3 != 1 {
		t.Errorf("Expected 1 Refueler for 5-day 8x8 match (fuel 60), got %d", refCount3)
	}

	// TH4: Map 32x32, 8 agents, 6 days, fuel 200 -> 1 Refueler + 7 Patrols (m-27005 scenario)
	s4 := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   make([]int, 6),
		FuelLimits: 200,
	}
	s4.Map.Width = 32
	s4.Map.Height = 32
	kinds4 := assignKinds(s4)
	refCount4 := 0
	for _, k := range kinds4 {
		if k == 1 {
			refCount4++
		}
	}
	if refCount4 != 1 {
		t.Errorf("Expected 1 Refueler for 6-day 32x32 match (fuel 200), got %d", refCount4)
	}

	// TH5: Map 32x32, 8 agents, 10 days, fuel 200 -> 2 Refuelers (very long match >= 9 days)
	s5 := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   make([]int, 10),
		FuelLimits: 200,
	}
	s5.Map.Width = 32
	s5.Map.Height = 32
	kinds5 := assignKinds(s5)
	refCount5 := 0
	for _, k := range kinds5 {
		if k == 1 {
			refCount5++
		}
	}
	if refCount5 != 2 {
		t.Errorf("Expected 2 Refuelers for 10-day 32x32 match (fuel 200), got %d", refCount5)
	}

	// TH6: Map 32x32, 8 agents, 6 days, fuel 120 -> 2 Refuelers (fuel < 150)
	s6 := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   make([]int, 6),
		FuelLimits: 120,
	}
	s6.Map.Width = 32
	s6.Map.Height = 32
	kinds6 := assignKinds(s6)
	refCount6 := 0
	for _, k := range kinds6 {
		if k == 1 {
			refCount6++
		}
	}
	if refCount6 != 2 {
		t.Errorf("Expected 2 Refuelers for 6-day 32x32 match (fuel 120), got %d", refCount6)
	}

	// TH7: Map 12x12, 6 agents, 10 days, fuel 100 -> 1 Refueler (m-26997 scenario)
	s7 := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5},
		DaySteps:   make([]int, 10),
		FuelLimits: 100,
	}
	s7.Map.Width = 12
	s7.Map.Height = 12
	kinds7 := assignKinds(s7)
	refCount7 := 0
	for _, k := range kinds7 {
		if k == 1 {
			refCount7++
		}
	}
	if refCount7 != 1 {
		t.Errorf("Expected 1 Refueler for 10-day 12x12 match with 6 agents, got %d", refCount7)
	}
}

func TestBuildPosRouteStartWait(t *testing.T) {
	s := &Setup{
		Agents:     []int{0, 1},
		DaySteps:   []int{30},
		FuelLimits: 60,
		Spots: []Spot{
			{Brand: 0, Pos: 10, Stocks: 2},
			{Brand: 1, Pos: 11, Stocks: 2},
		},
	}
	s.Map.Width = 8
	s.Map.Height = 8
	s.Map.Cells = make([][]int, 8)
	for r := 0; r < 8; r++ {
		s.Map.Cells[r] = make([]int, 8)
	}

	gs := NewGameState(s)
	gs.AgentPos[0] = 10 // Agent 0 starts on Spot 0 (pos 10)
	gs.AgentFuel[0] = 60
	gs.AgentKinds[0] = 0

	cands := []Candidate{
		{SpotIdx: 0, Pos: 10, Brand: 0, Value: 1.0},
		{SpotIdx: 1, Pos: 11, Brand: 1, Value: 1.0},
	}
	routeIdxs := []int{0, 1}

	srcs := []int{10, 11}
	dc := BuildDayCaches(srcs, gs)

	route, hasStartWait := buildPosRoute(0, routeIdxs, cands, gs, dc)
	if !hasStartWait {
		t.Errorf("Expected hasStartWait to be true when candidate 0 is at agentPos")
	}
	if len(route) < 2 {
		t.Errorf("Expected route to contain path to candidate 1, got %v", route)
	}

	// Verify EncodeRoute generates WAIT (-1) at the start
	var waitPoints []WaitPoint
	if hasStartWait {
		waitPoints = append(waitPoints, WaitPoint{Pos: 10, WaitSteps: 1})
	}
	actions := EncodeRoute(EncodeOpts{
		Route:      route,
		Budget:     30,
		StartFuel:  60,
		IsRefueler: false,
		WaitPoints: waitPoints,
		Gs:         gs,
	})

	if len(actions) == 0 || actions[0] != -1 {
		t.Errorf("Expected first action to be -1 (1-step wait), got %v", actions)
	}

	// Verify scoreActions scores candidate 0 at start pos
	allActions := [][]int{actions, {-30}}
	matchT, todayT, stocks := scoreActions(allActions, gs, make(map[int]bool), 0)
	if stocks < 2 {
		t.Errorf("Expected at least 2 stocks harvested (start spot + cand 1), got stocks=%d (matchT=%d, todayT=%d)", stocks, matchT, todayT)
	}

	// Verify updateBrandTracker records brand 0
	bt := NewBrandTracker()
	updateBrandTracker(bt, allActions, 0, gs)
	if !bt.Collected[0] {
		t.Errorf("Expected brand 0 to be recorded in bt.Collected, got false")
	}
}

func TestAssignDay24x24MultiRefueler(t *testing.T) {
	// Setup 24x24 map, 8 agents (6 Patrols, 2 Refuelers), 12 spots
	W, H := 24, 24
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100},
		FuelLimits: 200,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}

	// 12 spots with 4 stocks each
	for i := 0; i < 12; i++ {
		pos := (i%4+1)*4*W + (i/4+1)*4
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 6,
			Pos:    pos,
			Stocks: 4,
		})
	}

	gs := NewGameState(s)
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

	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)

	// Simulate Day 8 scenario where Agent 0 and Agent 1 have ~72 fuel
	gs.AgentFuel[0] = 72
	gs.AgentFuel[1] = 73

	assignment := AssignDay(context.Background(), 100, make(map[int]bool), gs, dc, nil)
	if len(assignment.Plans) != 8 {
		t.Fatalf("Expected 8 plans, got %d", len(assignment.Plans))
	}

	for _, p := range assignment.Plans {
		if len(p.Route) == 0 {
			t.Errorf("Agent %d has empty route", p.AgentIdx)
		}
	}

	actions := EncodePlan(assignment.Plans, 0, gs)
	_, _, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("stocks harvested in 24x24 test: %d / 48", stocks)

	for _, p := range assignment.Plans {
		visitedSpots := []int{}
		for _, pos := range p.Route {
			si := gs.SpotIndexAt(pos)
			if si >= 0 {
				visitedSpots = append(visitedSpots, si)
			}
		}
		t.Logf("Agent %d (kind=%d, fuel=%d): routeLen=%d, visitedSpots=%v", p.AgentIdx, gs.AgentKinds[p.AgentIdx], gs.FuelOf(p.AgentIdx), len(p.Route), visitedSpots)
	}
}

func BenchmarkAssignDay24x24(b *testing.B) {
	W, H := 24, 24
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100},
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
	for i := 0; i < 6; i++ {
		gs.AgentKinds[i] = 0
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 200
	}
	for i := 6; i < 8; i++ {
		gs.AgentKinds[i] = 1
		gs.AgentPos[i] = s.Spots[i+2].Pos
		gs.AgentFuel[i] = 0
	}
	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)
	gs.AgentFuel[0] = 72
	gs.AgentFuel[1] = 73

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		AssignDay(context.Background(), 100, make(map[int]bool), gs, dc, nil)
	}
}

func TestGRASPRandomIterations24x24(t *testing.T) {
	W, H := 24, 24
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   make([]int, 10),
		FuelLimits: 200,
	}
	for i := range s.DaySteps {
		s.DaySteps[i] = 100
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
	for i := 0; i < 6; i++ {
		gs.AgentKinds[i] = 0
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 200
	}
	for i := 6; i < 8; i++ {
		gs.AgentKinds[i] = 1
		gs.AgentPos[i] = s.Spots[i+2].Pos
		gs.AgentFuel[i] = 0
	}
	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)
	gs.AgentFuel[0] = 72
	gs.AgentFuel[1] = 73

	// Assume all brands already collected (like Day 1 to 9)
	allBrands := make(map[int]bool)
	for i := 0; i < 6; i++ {
		allBrands[i] = true
	}

	bestStocks := 0
	minStocks := 999
	sumStocks := 0
	iters := 50
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < iters; i++ {
		assignment := AssignDay(context.Background(), 100, allBrands, gs, dc, rng)
		actions := EncodePlan(assignment.Plans, 1, gs)
		_, _, stocks := scoreActions(actions, gs, allBrands, 1)
		if stocks > bestStocks {
			bestStocks = stocks
		}
		if stocks < minStocks {
			minStocks = stocks
		}
		sumStocks += stocks
	}
	t.Logf("GRASP 50 iters on 24x24 (all brands collected): best=%d, min=%d, avg=%.1f",
		bestStocks, minStocks, float64(sumStocks)/float64(iters))
}

func TestAssignDay8x8(t *testing.T) {
	// Map 8x8, 4 agents, 8 spots, 2 stocks each
	W, H := 8, 8
	s := &Setup{
		Agents:     []int{0, 1, 2, 3},
		DaySteps:   []int{30},
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
	for i := 0; i < 3; i++ {
		gs.AgentKinds[i] = 0 // Patrol
		gs.AgentPos[i] = s.Spots[i].Pos
		gs.AgentFuel[i] = 60
	}
	gs.AgentKinds[3] = 1 // Refueler
	gs.AgentPos[3] = s.Spots[4].Pos
	gs.AgentFuel[3] = 0

	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)

	assignment := AssignDay(context.Background(), 30, make(map[int]bool), gs, dc, nil)
	if len(assignment.Plans) != 4 {
		t.Fatalf("Expected 4 plans, got %d", len(assignment.Plans))
	}
	actions := EncodePlan(assignment.Plans, 0, gs)
	_, _, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("8x8 map stocks harvested: %d / 16", stocks)
	if stocks < 12 {
		t.Errorf("Expected at least 12 stocks on 8x8, got %d", stocks)
	}
}

func TestAssignDay32x32(t *testing.T) {
	// Map 32x32, 8 agents (6 Patrols, 2 Refuelers), 12 spots, 4 stocks each
	W, H := 32, 32
	s := &Setup{
		Agents:     []int{0, 1, 2, 3, 4, 5, 6, 7},
		DaySteps:   []int{100},
		FuelLimits: 200,
	}
	s.Map.Width = W
	s.Map.Height = H
	s.Map.Cells = make([][]int, H)
	for r := 0; r < H; r++ {
		s.Map.Cells[r] = make([]int, W)
	}
	for i := 0; i < 12; i++ {
		pos := (i%4+1)*6*W + (i/4+1)*6
		s.Spots = append(s.Spots, Spot{
			Brand:  i % 6,
			Pos:    pos,
			Stocks: 4,
		})
	}
	gs := NewGameState(s)
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

	srcs := CollectSources(gs)
	dc := BuildDayCaches(srcs, gs)

	assignment := AssignDay(context.Background(), 100, make(map[int]bool), gs, dc, nil)
	if len(assignment.Plans) != 8 {
		t.Fatalf("Expected 8 plans, got %d", len(assignment.Plans))
	}
	actions := EncodePlan(assignment.Plans, 0, gs)
	_, _, stocks := scoreActions(actions, gs, make(map[int]bool), 0)
	t.Logf("32x32 map stocks harvested: %d / 48", stocks)
	if stocks < 40 {
		t.Errorf("Expected at least 40 stocks on 32x32, got %d", stocks)
	}
}

