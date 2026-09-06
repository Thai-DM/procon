# HEXUDON Bot — Go 1.21+ (HTTP polling)

Bot tham chiến giải đấu PTIT Procon 2026 (HEXUDON).
Được viết bằng Go thuần (chỉ dùng thư viện chuẩn, không dependency ngoài).
Kiến trúc Low-latency, Modular, và xử lý chặt chẽ các ngoại lệ của API.

## Cài đặt & Chạy
Bạn cần cài đặt Go (phiên bản 1.21 trở lên).

```bash
# Chạy trực tiếp:
go run . <BASE_URL> <MATCH_ID> <TOKEN>

# Ví dụ:
go run . http://<judge-host>:8099 m-0001 m-0001-a-xxxxxxxx
```

Build thành binary để tối ưu hiệu năng:
```bash
go build -o hexudon-bot .
./hexudon-bot <BASE_URL> <MATCH_ID> <TOKEN>
```

## Cấu trúc Mã nguồn (Modular Architecture)
Bot đã được tái cấu trúc từ code mẫu ban đầu, chia thành các layer rõ ràng:

- `main.go` — Entry point: đọc args, khởi tạo, orchestrate vòng lặp trận đấu.
- `types.go` — Định nghĩa chính xác các struct JSON (Setup, Agent, DayState) theo spec API.
- `network.go` — NetworkClient: HTTP client với cơ chế Retry/Backoff phân loại lỗi đúng chuẩn (425, 429, 5xx, 4xx).
- `gamestate.go` — Quản lý trạng thái: carry-over vị trí, nhiên liệu, và tự theo dõi kho udon nội bộ độc lập.
- `hex.go` — Hình học lục giác Even-R, tính chi phí địa hình (Bảng 1), và ValidateMove.
- `pathfind.go` — Dijkstra all-pairs song song hoá (Goroutines) tính toán chi phí (Step, Fuel).
- `encode.go` — Chuyển đổi route thành chuỗi hành động hợp lệ, đệm đủ ngân sách Step.
- `assign.go` — Task Assignment: lai giữa Exact DP (Knapsack) và Heuristic (Cheapest Insertion + 2-opt), cùng Rendezvous Scheduling cho xe tiếp nhiên liệu.
- `plan.go` — Vòng lặp Anytime Planning và Greedy Maximum Coverage tối đa hóa tiêu chí thắng.

## Chiến thuật
1. **Quản lý Udon Nội bộ**: Tự track số lượng udon còn lại của điểm thu hoạch thay vì phụ thuộc vào server (vì server không cung cấp realtime).
2. **Pathfinding & Allocation**: Dùng Dijkstra cache 2 chiều chi phí (fuel & step), phân công công việc qua giải thuật DP hoặc Heuristic tùy thuộc vào số lượng điểm lấy Udon.
3. **Rendezvous Refuel**: Xe nạp nhiên liệu dự đoán điểm cạn kiệt nhiên liệu của Patrol để lên lịch điểm hẹn nạp.
4. **Anytime Algorithm**: Bot luôn nộp một kết quả an toàn ngay lập tức (không trễ deadline), sau đó liên tục cải thiện và nộp đè lên kết quả tốt hơn trong suốt thời gian ngân sách cho phép.
# procon
