// profile.go — Phân loại 5 cấp độ Map chuẩn (8x8, 12x12, 16x16, 24x24, 32x32) và Dynamic Day Budgeting.
package main

import (
	"time"
)

// MapProfile chứa các tham số được tinh chỉnh tối ưu cho từng loại kích thước map và số ngày thi đấu.
type MapProfile struct {
	Name                   string        // Tên Profile (ví dụ "P1: Micro (8x8)")
	DayTimeout             time.Duration // Ngân sách thời gian suy nghĩ mỗi ngày
	DpThreshold            int           // Ngưỡng bãi ứng viên tối đa cho đệ quy DFS
	StepFactor             float64       // Hệ số phạt khoảng cách bước di chuyển
	NewBrandBonus          float64       // Điểm thưởng cho Brand mới chưa từng thu toàn trận
	LowFuelRefuelThreshold int           // Ngưỡng xăng cạn của Patrol để Refueler bám sát cứu
	MaxGRASPIterations     int           // Số lượt lặp tối đa của GRASP
	MaxNoImprove           int           // Số lượt lặp không cải thiện trước khi dừng GRASP
}

var CustomDayTimeout time.Duration = 0

// GetMapProfile trả về cấu hình tối ưu nhất dựa trên GameState.
func GetMapProfile(gs *GameState) *MapProfile {
	maxDim := gs.Width()
	if gs.Height() > maxDim {
		maxDim = gs.Height()
	}

	var prof *MapProfile

	// 2. Phân loại 5 Map Profiles dựa trên kích thước maxDim
	if maxDim <= 8 {
		// P1: Micro (8x8)
		// Đạt 100% trần lý thuyết ở iter 1-4, dừng sớm ở iter 20 (chỉ mất 8ms - 25ms!)
		prof = &MapProfile{
			Name:                   "P1: Micro (8x8)",
			DayTimeout:             350 * time.Millisecond,
			DpThreshold:            16,
			StepFactor:             0.10,
			NewBrandBonus:          10.0,
			LowFuelRefuelThreshold: 35,
			MaxGRASPIterations:     2000,
			MaxNoImprove:           80,
		}
	} else if maxDim <= 12 {
		// P2: Medium (12x12, 10x10, 9x9)
		prof = &MapProfile{
			Name:                   "P2: Medium (12x12)",
			DayTimeout:             800 * time.Millisecond,
			DpThreshold:            18,
			StepFactor:             0.07,
			NewBrandBonus:          12.0,
			LowFuelRefuelThreshold: 52,
			MaxGRASPIterations:     6000,
			MaxNoImprove:           300,
		}
	} else if maxDim <= 16 {
		// P3: Large (16x16)
		prof = &MapProfile{
			Name:                   "P3: Large (16x16)",
			DayTimeout:             900 * time.Millisecond,
			DpThreshold:            17,
			StepFactor:             0.05,
			NewBrandBonus:          15.0,
			LowFuelRefuelThreshold: 75,
			MaxGRASPIterations:     7000,
			MaxNoImprove:           300,
		}
	} else if maxDim <= 24 {
		// P4: Map 24x24
		threshold := 85
		if gs.DayStepsForDay(gs.CurrentDay) >= 80 {
			threshold = 120 // Khi budget lớn, xe tiêu tốn 50-70 fuel/ngày -> tiếp xăng chủ động từ sớm
		}
		prof = &MapProfile{
			Name:                   "P4: X-Large (24x24)",
			DayTimeout:             1050 * time.Millisecond,
			DpThreshold:            16,
			StepFactor:             0.01,
			NewBrandBonus:          18.0,
			LowFuelRefuelThreshold: threshold,
			MaxGRASPIterations:     9000,
			MaxNoImprove:           500,
		}
	} else {
		// P5: Huge (32x32+)
		// Map 32x32 có 1024 ô, không thể ăn 100% bãi.
		// CHẤT LƯỢNG LÊN HÀNG ĐẦU: Dành 1150ms và MaxNoImprove=1000 để GRASP tìm sâu nhất số Udon!
		threshold := 90
		if gs.DayStepsForDay(gs.CurrentDay) >= 80 {
			threshold = 130
		}
		prof = &MapProfile{
			Name:                   "P5: Huge (32x32+)",
			DayTimeout:             1150 * time.Millisecond,
			DpThreshold:            16,
			StepFactor:             0.005,
			NewBrandBonus:          25.0,
			LowFuelRefuelThreshold: threshold,
			MaxGRASPIterations:     15000,
			MaxNoImprove:           1000,
		}
	}

	if CustomDayTimeout > 0 {
		prof.DayTimeout = CustomDayTimeout
	}
	return prof
}
