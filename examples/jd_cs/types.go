package jd_cs

import "time"

type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderPaid      OrderStatus = "paid"
	OrderShipped   OrderStatus = "shipped"
	OrderDelivered OrderStatus = "delivered"
	OrderCompleted OrderStatus = "completed"
	OrderCancelled OrderStatus = "cancelled"
	OrderReturned  OrderStatus = "returned"
)

type Order struct {
	OrderID     string      `json:"order_id"`
	UserID      string      `json:"user_id"`
	ProductName string      `json:"product_name"`
	Price       float64     `json:"price"`
	Quantity    int         `json:"quantity"`
	Status      OrderStatus `json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	Address     string      `json:"address"`
	Phone       string      `json:"phone"`
	TrackingNum string      `json:"tracking_number,omitempty"`
}

type User struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	Level    string `json:"level"` // gold, silver, bronze
	Points   int    `json:"points"`
	JoinDate string `json:"join_date"`
}

type Product struct {
	ProductID   string  `json:"product_id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Price       float64 `json:"price"`
	Stock       int     `json:"stock"`
	Description string  `json:"description"`
	Rating      float64 `json:"rating"`
}

type RefundRequest struct {
	RequestID   string    `json:"request_id"`
	OrderID     string    `json:"order_id"`
	UserID      string    `json:"user_id"`
	Reason      string    `json:"reason"`
	Amount      float64   `json:"amount"`
	Status      string    `json:"status"` // pending, approved, rejected
	CreatedAt   time.Time `json:"created_at"`
	ProcessedAt time.Time `json:"processed_at,omitempty"`
}

type LogisticsInfo struct {
	TrackingNum string     `json:"tracking_number"`
	Status      string     `json:"status"`
	Location    string     `json:"location"`
	EstArrival  string     `json:"estimated_arrival"`
	Events      []LogEvent `json:"events"`
}

type LogEvent struct {
	Time    string `json:"time"`
	Event   string `json:"event"`
	Station string `json:"station"`
}

type CSRQuery struct {
	UserID  string `json:"user_id"`
	OrderID string `json:"order_id,omitempty"`
	Query   string `json:"query"`
}

type CSRResponse struct {
	Answer     string  `json:"answer"`
	Action     string  `json:"action,omitempty"`
	Confidence float64 `json:"confidence"`
}

type IntentType string

const (
	IntentQueryOrder     IntentType = "query_order"
	IntentTrackLogistics IntentType = "track_logistics"
	IntentRefund         IntentType = "refund"
	IntentRecommend      IntentType = "recommend_product"
	IntentComplaint      IntentType = "complaint"
	IntentTransfer       IntentType = "transfer_human"
	IntentGreeting       IntentType = "greeting"
	IntentUnknown        IntentType = "unknown"
)
