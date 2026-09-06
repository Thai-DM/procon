// types.go — Toàn bộ struct JSON theo đặc tả BTC chính xác.
// KHÔNG suy diễn thêm field ngoài spec.
package main

import "encoding/json"

// Setup — cấu hình trận đấu, lấy từ GET /setup.
// ⚠️  Setup.Agents CHỈ dùng len() để lấy số lượng agent;
//
//	KHÔNG chứa vị trí hay loại thật (lấy từ DayState ngày 0).
type Setup struct {
	Map struct {
		Height int     `json:"height"`
		Width  int     `json:"width"`
		Cells  [][]int `json:"cells"` // 0=đất 1=đường 2=núi 3=ao
	} `json:"map"`
	Spots      []Spot `json:"spots"`
	Agents     []int  `json:"agents"` // chỉ dùng len()
	DaySteps   []int  `json:"daySteps"`
	FuelLimits int    `json:"fuelLimits"` // giới hạn fuel Patrol, đọc từ server, KHÔNG hardcode
}

// Spot — điểm thu udon.
type Spot struct {
	Brand  int `json:"brand"`
	Pos    int `json:"pos"`
	Stocks int `json:"stocks"` // kho tối đa gốc (server KHÔNG trả kho hiện tại — bot tự track)
}

// Agent — trạng thái agent trong ngày từ DayState.
// ⚠️  Fuel là con trỏ nullable: Refueler có thể trả null,
//
//	PHẢI nil-check trước khi deref.
type Agent struct {
	Kind int  `json:"kind"` // 0=Patrol 1=Refueler
	Pos  int  `json:"pos"`
	Fuel *int `json:"fuel"`
}

// FuelVal trả fuel an toàn, fallback về defaultVal nếu nil.
func (a *Agent) FuelVal(defaultVal int) int {
	if a.Fuel != nil {
		return *a.Fuel
	}
	return defaultVal
}

// DayState — trạng thái đầu mỗi ngày từ GET /state.
// ⚠️  Others là OPTIONAL — một số cấu hình BTC không trả field này.
//
//	Dùng json.RawMessage để parse an toàn, không crash nếu vắng mặt.
//	TUYỆT ĐỐI KHÔNG dùng fuel/thông tin đối thủ để lập kế hoạch fuel của mình.
type DayState struct {
	Day      int             `json:"day"`
	Agents   []Agent         `json:"agents"`
	Others   json.RawMessage `json:"others,omitempty"` // optional, parse an toàn
	Traffics []TrafficEntry  `json:"traffics"`
}

// TrafficEntry — trạng thái giao thông 1 ô đường trong ngày.
// Server tự tính và cấp mỗi đầu ngày — bot chỉ đọc, KHÔNG tự suy luận.
type TrafficEntry struct {
	Pos    int `json:"pos"`
	Status int `json:"status"` // 0=thông thoáng 1=đông đúc 2=ùn tắc
}

// ActionResult — kết quả sau POST /actions.
// Một agent sai → CẢ LƯỢT bị từ chối.
type ActionResult struct {
	Ok     bool   `json:"ok"`
	Reason string `json:"reason"` // E_NOT_ADJACENT|E_POND|E_STEP_OVERFLOW|E_NO_FUEL|E_BAD_FORMAT|E_RATE_LIMIT
}
