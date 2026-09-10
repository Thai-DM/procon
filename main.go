// HEXUDON Bot — Go 1.21+
// Chạy HTTP (cũ): go run . <BASE_URL> <MATCH_ID> <TOKEN>
// Chạy HTTP:      go run . -transport http -url <URL> -match <MATCH> -token <TOKEN>
// Chạy WS:        go run . -transport ws   -url <URL> -match <MATCH> -token <TOKEN>
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// brandTracker theo dõi brand đã thu toàn trận (tích luỹ qua các ngày).
var brandTracker *BrandTracker

func main() {
	// --- Parse flags ---
	// Hỗ trợ cả 2 kiểu CLI:
	//   Cũ:  go run . <URL> <MATCH> <TOKEN>
	//   Mới: go run . -transport ws -url <URL> -match <MATCH> -token <TOKEN>
	transportFlag := flag.String("transport", "http", "Transport: http | ws")
	urlFlag := flag.String("url", "", "Base URL server")
	matchFlag := flag.String("match", "", "Match ID")
	tokenFlag := flag.String("token", "", "API Token")
	flag.Parse()

	var base, matchID, token string
	positional := flag.Args()
	switch {
	case *urlFlag != "" && *matchFlag != "" && *tokenFlag != "":
		// Kiểu flag mới (giống sample-bot)
		base, matchID, token = *urlFlag, *matchFlag, *tokenFlag
	case len(positional) >= 3:
		// Positional args sau khi parse flags
		base, matchID, token = positional[0], positional[1], positional[2]
	default:
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s <BASE_URL> <MATCH_ID> <TOKEN>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -transport ws -url <URL> -match <MATCH> -token <TOKEN>\n", os.Args[0])
		os.Exit(1)
	}

	// Graceful shutdown khi nhận SIGINT / SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Tạo transport ---
	var tr GameTransport
	var wsClient *WsNetworkClient

	if *transportFlag == "ws" {
		log.Printf("[INIT] Transport: WebSocket (server-push, low-latency)")
		wsClient = NewWsNetworkClient(base, matchID, token)
		tr = wsClient
	} else {
		log.Printf("[INIT] Transport: HTTP (polling)")
		tr = NewNetworkClient(base, matchID, token)
	}

	// 1. Kết nối WebSocket (nếu dùng WS)
	// Server WS sẽ push `setup` ngay sau khi connect thành công.
	if wsClient != nil {
		if err := wsClient.Connect(ctx); err != nil {
			log.Printf("[WARN] WS connect failed (%v), falling back to HTTP", err)
			tr = NewNetworkClient(base, matchID, token)
			wsClient = nil
		} else {
			defer wsClient.Close()
		}
	}

	// 2. Lấy cấu hình trận
	//   HTTP: gọi GET /setup
	//   WS:   chờ server đẩy message chứa map/daySteps
	log.Println("[INIT] Fetching setup...")
	setup, err := tr.GetSetup(ctx)
	if err != nil {
		log.Fatalf("[FATAL] GetSetup: %v", err)
	}
	totalStocks := 0
	for _, sp := range setup.Spots {
		totalStocks += sp.Stocks
	}
	log.Printf("[INIT] Map %dx%d | agents=%d | spots=%d (maxStocks/day=%d) | days=%d | fuelLimit=%d",
		setup.Map.Width, setup.Map.Height,
		len(setup.Agents), len(setup.Spots), totalStocks,
		len(setup.DaySteps), setup.FuelLimits)

	// 3. Khởi tạo GameState + BrandTracker
	gs := NewGameState(setup)
	brandTracker = NewBrandTracker()

	// 4. Gán loại agent
	kinds := assignKinds(setup)
	log.Printf("[INIT] Assigning kinds: %v", kinds)
	if err := tr.PostAssignment(ctx, kinds); err != nil {
		log.Fatalf("[FATAL] PostAssignment: %v", err)
	}
	gs.SetKinds(kinds)

	// 5. Chờ trận bắt đầu
	//   HTTP: poll 200ms đến khi server hết trả 425
	//   WS:   block cho đến khi server PUSH "start" → 0ms overhead
	log.Println("[INIT] Waiting for match to start...")
	if err := tr.WaitStart(ctx); err != nil {
		log.Fatalf("[FATAL] WaitStart: %v", err)
	}
	log.Println("[INIT] Match started!")

	// 6. Vòng lặp trận đấu
	//   HTTP: liên tục poll GET /state, sleep 200ms giữa các lần
	//   WS:   chỉ cần block recv() — server push ngay khi ngày mới bắt đầu
	lastDay := -1
	for {
		select {
		case <-ctx.Done():
			log.Println("[EXIT] Context cancelled, shutting down.")
			return
		default:
		}

		st, err := tr.GetState(ctx)
		if err != nil {
			log.Printf("[INFO] GetState: %v — trying result endpoint...", err)
			break
		}

		if st.Day == lastDay {
			// HTTP: cùng ngày → poll tiếp. WS: không xảy ra.
			continue
		}

		// --- Ngày mới ---
		if !gs.Initialized {
			gs.InitDay0(st)
			log.Printf("[DAY 0] Agent positions: %v | fuels: %v", gs.AgentPos, gs.AgentFuel)
		} else {
			gs.NewDay(st)
		}
		log.Printf("[DAY %d] DEBUG positions: %v | fuels: %v", st.Day, gs.AgentPos, gs.AgentFuel)
		for aIdx, fVal := range gs.AgentFuel {
			if gs.IsPatrol(aIdx) && fVal < 15 {
				log.Printf("[WARNING] CRITICAL LOW FUEL ALERT: Day %d Agent %d has ONLY %d fuel remaining!", st.Day, aIdx, fVal)
			}
		}
		log.Printf("[DAY %d] budget=%d steps | %d road statuses",
			st.Day, gs.DayStepsForDay(st.Day), len(st.Traffics))

		// --- Lập kế hoạch & gửi hành động ---
		RunDayWithTransport(ctx, st, gs, brandTracker, tr)

		lastDay = st.Day

		if lastDay == len(setup.DaySteps)-1 {
			log.Println("[INFO] Match has reached the final day. Waiting for results...")
			break
		}
	}

	// 7. Lấy kết quả cuối trận
	if res, rerr := tr.GetResult(ctx); rerr == nil {
		fmt.Println("\n[RESULT] Match finished successfully!")
		fmt.Println(string(res))
	} else {
		log.Printf("[WARN] GetResult: %v", rerr)
	}
}

// assignKinds quyết định loại agent cho cả trận.
func assignKinds(setup *Setup) []int {
	n := len(setup.Agents)
	kinds := make([]int, n)
	if n <= 1 {
		return kinds
	}

	days := len(setup.DaySteps)
	maxDim := setup.Map.Width
	if setup.Map.Height > maxDim {
		maxDim = setup.Map.Height
	}

	// Tính ngân sách bước lớn nhất trong trận (budget tối đa)
	maxBudget := 0
	for _, s := range setup.DaySteps {
		if s > maxBudget {
			maxBudget = s
		}
	}

	// Ước tính mức tiêu hao xăng mỗi ngày của 1 Patrol:
	// - Trên map nhỏ (<=8, <=12), khoảng cách giữa các bãi ngắn, bước thực tế rất ít
	dailyFuelPerPatrol := float64(maxBudget) * 0.8
	if maxDim <= 8 {
		if dailyFuelPerPatrol > 13.0 {
			dailyFuelPerPatrol = 13.0
		}
	} else if maxDim <= 12 {
		if dailyFuelPerPatrol > 16.0 {
			dailyFuelPerPatrol = 16.0
		}
	}
	totalFuelNeeded := dailyFuelPerPatrol * float64(days)
	needsRefuel := totalFuelNeeded > float64(setup.FuelLimits)

	numRefuelers := 1

	// TH1: Bình xăng lớn (FuelLimits >= 100)
	if setup.FuelLimits >= 100 {
		if maxDim >= 24 {
			// Map to 24x24 & 32x32+ (8 xe):
			// - Trận dài (>= 6 ngày) HOẶC bình xăng nhỏ (< 150): 2 Refuelers + 6 Patrols để đảm bảo an toàn đường dài.
			// - Trận ngắn (<= 5 ngày) VÀ bình xăng dồi dào (>= 150): 1 Refueler + 7 Patrols để tối đa hóa số xe thu hoạch Udon!
			//   Phase 5 Safety Guard đã chặn tuyệt đối E_NO_FUEL, 7 Patrols giúp áp đảo đối thủ về sản lượng.
			if days >= 6 || setup.FuelLimits < 150 {
				if n >= 7 {
					numRefuelers = 2
				} else {
					numRefuelers = 1
				}
			} else {
				numRefuelers = 1
			}
		} else {
			// Map trung bình/nhỏ (16x16 trở xuống, e.g. 6 xe):
			// 1 Refueler gánh 5 Patrols (Kiểm chứng rất tốt ở map 16x16)
			if n >= 7 {
				numRefuelers = 2
			}
		}
	} else {
		// TH2: Bình xăng nhỏ (FuelLimits < 100)
		if n >= 5 {
			if maxDim >= 12 || n >= 6 {
				numRefuelers = 2
			}
		}
		if n >= 8 && maxDim >= 24 {
			numRefuelers = 3
		}
	}

	// TH3: Map siêu nhỏ (8x8) và ít xe (<= 4 xe):
	// Nếu bình xăng đủ dùng cả trận (FuelLimits >= days * maxBudget * 0.8), dùng 100% Patrols!
	if maxDim <= 8 && n <= 4 && !needsRefuel {
		numRefuelers = 0
	}

	for i := 0; i < numRefuelers; i++ {
		kinds[n-1-i] = 1
	}
	return kinds
}