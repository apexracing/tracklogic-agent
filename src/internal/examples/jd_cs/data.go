package jd_cs

import "time"

var MockUsers = map[string]User{
	"u1001": {UserID: "u1001", Name: "张三", Phone: "13800138001", Level: "gold", Points: 5200, JoinDate: "2023-06-15"},
	"u1002": {UserID: "u1002", Name: "李四", Phone: "13900139002", Level: "silver", Points: 1200, JoinDate: "2024-01-20"},
	"u1003": {UserID: "u1003", Name: "王五", Phone: "13700137003", Level: "bronze", Points: 300, JoinDate: "2025-03-10"},
}

var MockOrders = map[string]Order{
	"ord1001": {
		OrderID: "ord1001", UserID: "u1001", ProductName: "iPhone 16 Pro Max",
		Price: 9999, Quantity: 1, Status: OrderDelivered,
		CreatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local),
		Address: "北京市朝阳区建国路88号", Phone: "13800138001",
		TrackingNum: "SF1234567890",
	},
	"ord1002": {
		OrderID: "ord1002", UserID: "u1002", ProductName: "MacBook Air M4",
		Price: 8999, Quantity: 1, Status: OrderShipped,
		CreatedAt: time.Date(2026, 6, 15, 14, 30, 0, 0, time.Local),
		Address: "上海市浦东新区张江高科技园区", Phone: "13900139002",
		TrackingNum: "SF0987654321",
	},
	"ord1003": {
		OrderID: "ord1003", UserID: "u1001", ProductName: "AirPods Pro 2",
		Price: 1999, Quantity: 2, Status: OrderPaid,
		CreatedAt: time.Date(2026, 6, 20, 9, 15, 0, 0, time.Local),
		Address: "北京市朝阳区建国路88号", Phone: "13800138001",
	},
	"ord1004": {
		OrderID: "ord1004", UserID: "u1003", ProductName: "机械键盘 K8 Pro",
		Price: 599, Quantity: 1, Status: OrderPending,
		CreatedAt: time.Date(2026, 6, 25, 16, 45, 0, 0, time.Local),
		Address: "广州市天河区体育西路100号", Phone: "13700137003",
	},
	"ord1005": {
		OrderID: "ord1005", UserID: "u1002", ProductName: "戴尔 U2724D 显示器",
		Price: 3299, Quantity: 1, Status: OrderDelivered,
		CreatedAt: time.Date(2026, 5, 10, 11, 20, 0, 0, time.Local),
		Address: "上海市浦东新区张江高科技园区", Phone: "13900139002",
		TrackingNum: "SF5566778899",
	},
}

var MockProducts = map[string]Product{
	"p001": {ProductID: "p001", Name: "iPhone 16 Pro Max", Category: "手机", Price: 9999, Stock: 128, Description: "Apple最新旗舰手机，A18 Pro芯片", Rating: 4.8},
	"p002": {ProductID: "p002", Name: "MacBook Air M4", Category: "笔记本", Price: 8999, Stock: 56, Description: "Apple最新轻薄笔记本，M4芯片", Rating: 4.9},
	"p003": {ProductID: "p003", Name: "AirPods Pro 2", Category: "耳机", Price: 1999, Stock: 200, Description: "主动降噪无线耳机", Rating: 4.7},
	"p004": {ProductID: "p004", Name: "机械键盘 K8 Pro", Category: "外设", Price: 599, Stock: 89, Description: "87键无线机械键盘，蓝牙双模", Rating: 4.5},
	"p005": {ProductID: "p005", Name: "戴尔 U2724D 显示器", Category: "显示器", Price: 3299, Stock: 34, Description: "27寸 4K IPS 专业显示器", Rating: 4.6},
	"p006": {ProductID: "p006", Name: "华为 MatePad Pro 13.2", Category: "平板", Price: 5699, Stock: 42, Description: "华为旗舰平板，OLED屏", Rating: 4.7},
}

var MockRefunds = map[string]RefundRequest{
	"ref1001": {
		RequestID: "ref1001", OrderID: "ord1005", UserID: "u1002",
		Reason: "显示器有一个坏点", Amount: 3299,
		Status: "pending", CreatedAt: time.Date(2026, 6, 22, 10, 0, 0, 0, time.Local),
	},
}

var MockLogistics = map[string]LogisticsInfo{
	"SF1234567890": {
		TrackingNum: "SF1234567890", Status: "已签收", Location: "北京朝阳区",
		EstArrival: "2026-06-05",
		Events: []LogEvent{
			{Time: "2026-06-01 18:00", Event: "已揽收", Station: "北京朝阳配送中心"},
			{Time: "2026-06-02 10:00", Event: "运输中", Station: "北京中转站"},
			{Time: "2026-06-03 08:00", Event: "派送中", Station: "北京朝阳配送站"},
			{Time: "2026-06-04 14:00", Event: "已签收", Station: "用户本人"},
		},
	},
	"SF0987654321": {
		TrackingNum: "SF0987654321", Status: "运输中", Location: "上海浦东新区",
		EstArrival: "2026-06-28",
		Events: []LogEvent{
			{Time: "2026-06-25 20:00", Event: "已揽收", Station: "上海浦东配送中心"},
			{Time: "2026-06-26 09:00", Event: "运输中", Station: "上海中转站"},
		},
	},
	"SF5566778899": {
		TrackingNum: "SF5566778899", Status: "已签收", Location: "上海浦东新区",
		EstArrival: "2026-05-14",
		Events: []LogEvent{
			{Time: "2026-05-10 20:00", Event: "已揽收", Station: "上海浦东配送中心"},
			{Time: "2026-05-12 10:00", Event: "已签收", Station: "用户本人"},
		},
	},
}
