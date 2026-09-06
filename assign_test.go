package main

import (
	"context"
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
}

