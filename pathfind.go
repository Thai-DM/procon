// pathfind.go — Dijkstra all-pairs cache cho agent/spots/điểm hẹn refuel.
// Tính lại đầu mỗi ngày (traffic thay đổi). Song song hoá bằng goroutine + WaitGroup.
package main

import (
	"container/heap"
	"sync"
)

// ---------------------------------------------------------------------------
// Dijkstra với 2 chiều chi phí: (stepCost, fuelCost).
// Priority queue theo stepCost (tối thiểu step trước).
// fuelCost chỉ dùng để kiểm tra feasibility, không làm heuristic.
// ---------------------------------------------------------------------------

// distNode là 1 phần tử trong priority queue.
type distNode struct {
	pos      int
	steps    int // tổng step tích luỹ
	fuel     int // tổng fuel tích luỹ
	index    int // vị trí trong heap
}

type distHeap []*distNode

func (h distHeap) Len() int            { return len(h) }
func (h distHeap) Less(i, j int) bool { return h[i].steps < h[j].steps }
func (h distHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *distHeap) Push(x any) {
	n := (*h)
	item := x.(*distNode)
	item.index = len(n)
	*h = append(n, item)
}
func (h *distHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}

// PathInfo lưu chi phí đường đi ngắn nhất và chuỗi hướng từ src đến dst.
type PathInfo struct {
	Steps int   // tổng step
	Fuel  int   // tổng fuel tiêu tốn (theo Patrol — Refueler tự nhân 0)
	Dirs  []int // chuỗi hướng (0-5)
}

// Unreachable là sentinel cho ô không thể tới được.
var Unreachable = PathInfo{Steps: 1<<30, Fuel: 1<<30, Dirs: nil}

// dijkstraFrom chạy Dijkstra từ src, trả map[dst]PathInfo cho mọi ô có thể tới.
// isRefueler=true → fuelCost=0 mọi bước (Refueler không tốn fuel).
func dijkstraFrom(src int, isRefueler bool, gs *GameState) map[int]PathInfo {
	W, H := gs.Width(), gs.Height()
	total := W * H

	bestSteps := make([]int, total)
	bestFuel := make([]int, total)
	prevPos := make([]int, total)
	prevDir := make([]int, total)
	for i := range bestSteps {
		bestSteps[i] = 1 << 30
		bestFuel[i] = 1 << 30
		prevPos[i] = -1
		prevDir[i] = -1
	}
	bestSteps[src] = 0
	bestFuel[src] = 0

	h := &distHeap{}
	heap.Push(h, &distNode{pos: src, steps: 0, fuel: 0})

	for h.Len() > 0 {
		cur := heap.Pop(h).(*distNode)
		pos := cur.pos

		// Bỏ qua nếu đã tìm được đường tốt hơn
		if cur.steps > bestSteps[pos] {
			continue
		}

		for d := 0; d < 6; d++ {
			nb := neighbor(pos, d, W, H)
			if nb < 0 {
				continue
			}
			nbCell := gs.Cell(nb)
			if nbCell == 3 { // ao — không đi vào
				continue
			}

			// Chi phí tính theo ô NGUỒN (pos) theo luật chính thức
			srcCell := gs.Cell(pos)
			trafficStatus := gs.TrafficStatus(pos)
			sc, fc, canMove := TerrainCost(srcCell, trafficStatus)
			if !canMove {
				continue
			}
			if isRefueler {
				fc = 0
			}

			newSteps := cur.steps + sc
			newFuel := cur.fuel + fc
			
			maxFuelAllowed := gs.FuelLimit()
			if !isRefueler {
				for _, k := range gs.AgentKinds {
					if k == 1 {
						maxFuelAllowed = gs.FuelLimit() * 3
						break
					}
				}
			}

			// TUYỆT ĐỐI KHÔNG xét đường đi vượt quá bình xăng khi KHÔNG có Refueler
			if !isRefueler && newFuel > maxFuelAllowed {
				continue
			}

			if newSteps < bestSteps[nb] {
				bestSteps[nb] = newSteps
				bestFuel[nb] = newFuel
				prevPos[nb] = pos
				prevDir[nb] = d
				heap.Push(h, &distNode{pos: nb, steps: newSteps, fuel: newFuel})
			}
		}
	}

	// Trích xuất kết quả và tái tạo đường đi
	result := make(map[int]PathInfo, total)
	for dst := 0; dst < total; dst++ {
		if bestSteps[dst] == 1<<30 {
			continue // không tới được
		}
		// Truy ngược đường đi từ dst về src
		dirs := make([]int, 0, 8)
		for cur := dst; cur != src; {
			dirs = append([]int{prevDir[cur]}, dirs...)
			cur = prevPos[cur]
		}
		result[dst] = PathInfo{
			Steps: bestSteps[dst],
			Fuel:  bestFuel[dst],
			Dirs:  dirs,
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// DistCache — ma trận all-pairs chi phí giữa tập điểm quan trọng.
// Tập điểm: vị trí agent + spots + điểm hẹn refuel.
// Tính lại đầu mỗi ngày vì traffic thay đổi.
// ---------------------------------------------------------------------------

// DistCache lưu trữ kết quả Dijkstra từ mỗi nguồn trong tập điểm quan trọng.
type DistCache struct {
	// PathFrom[srcPos][dstPos] = PathInfo (step, fuel, dirs)
	// Chỉ tồn tại key nếu dstPos reachable từ srcPos.
	PathFrom map[int]map[int]PathInfo

	// Tập nguồn đã tính (để tránh tính lại nếu gọi nhiều lần)
	sources map[int]bool
	mu      sync.RWMutex
}

// NewDistCache tạo cache rỗng.
func NewDistCache() *DistCache {
	return &DistCache{
		PathFrom: make(map[int]map[int]PathInfo),
		sources:  make(map[int]bool),
	}
}

// Build tính Dijkstra từ tất cả sources song song bằng goroutine.
// isRefueler xác định cách tính fuel cost.
// Gọi đầu mỗi ngày trước khi plan.
func (dc *DistCache) Build(sources []int, isRefueler bool, gs *GameState) {
	// Reset cache
	dc.mu.Lock()
	dc.PathFrom = make(map[int]map[int]PathInfo, len(sources))
	dc.sources = make(map[int]bool, len(sources))
	dc.mu.Unlock()

	var wg sync.WaitGroup
	type result struct {
		src   int
		paths map[int]PathInfo
	}
	ch := make(chan result, len(sources))

	for _, src := range sources {
		// Dedup
		dc.mu.RLock()
		already := dc.sources[src]
		dc.mu.RUnlock()
		if already {
			continue
		}
		dc.mu.Lock()
		dc.sources[src] = true
		dc.mu.Unlock()

		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			ch <- result{src: s, paths: dijkstraFrom(s, isRefueler, gs)}
		}(src)
	}

	// Đóng channel sau khi tất cả goroutine xong
	go func() {
		wg.Wait()
		close(ch)
	}()

	// Thu kết quả
	for r := range ch {
		dc.mu.Lock()
		dc.PathFrom[r.src] = r.paths
		dc.mu.Unlock()
	}
}

// Get trả PathInfo từ src đến dst. ok=false nếu không tới được hoặc chưa tính.
func (dc *DistCache) Get(src, dst int) (PathInfo, bool) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	m, ok := dc.PathFrom[src]
	if !ok {
		return Unreachable, false
	}
	p, ok := m[dst]
	return p, ok
}

// Steps trả tổng step từ src đến dst, hoặc 1<<30 nếu không tới được.
func (dc *DistCache) Steps(src, dst int) int {
	p, ok := dc.Get(src, dst)
	if !ok {
		return 1 << 30
	}
	return p.Steps
}

// ---------------------------------------------------------------------------
// DayDistCaches — gộp cache cho Patrol và Refueler (chi phí fuel khác nhau).
// ---------------------------------------------------------------------------

// DayDistCaches chứa 2 cache riêng: 1 cho Patrol (có fuel cost), 1 cho Refueler.
type DayDistCaches struct {
	Patrol  *DistCache // isRefueler=false
	Refueler *DistCache // isRefueler=true
}

// BuildDayCaches tính all-pairs Dijkstra cho ngày mới.
// sources: tất cả pos cần làm nguồn (agent positions + spot positions).
// Song song hoá: Patrol và Refueler cache tính đồng thời.
func BuildDayCaches(sources []int, gs *GameState) *DayDistCaches {
	dc := &DayDistCaches{
		Patrol:   NewDistCache(),
		Refueler: NewDistCache(),
	}

	// Dedup sources
	seen := make(map[int]bool, len(sources))
	uniq := sources[:0:len(sources)]
	for _, s := range sources {
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		dc.Patrol.Build(uniq, false, gs)
	}()
	go func() {
		defer wg.Done()
		dc.Refueler.Build(uniq, true, gs)
	}()

	wg.Wait()
	return dc
}

// CollectSources thu thập tất cả pos làm nguồn Dijkstra:
// vị trí hiện tại của tất cả agent + vị trí tất cả spot.
func CollectSources(gs *GameState) []int {
	srcs := make([]int, 0, len(gs.AgentPos)+len(gs.Spots))
	srcs = append(srcs, gs.AgentPos...)
	for _, sp := range gs.Spots {
		srcs = append(srcs, sp.Pos)
	}
	return srcs
}
