package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/google/uuid"
)

type OrderStatus string // Sipariş durumları için type alias.

const (
	StatusPending   OrderStatus = "pending"
	StatusRejected  OrderStatus = "rejected"
	StatusPreparing OrderStatus = "preparing"
	StatusReady     OrderStatus = "ready"
)

type OrderItem struct {
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
}

type Order struct {
	ID           string      `json:"id"`
	UserID       int64       `json:"user_id"`
	RestaurantID int64       `json:"restaurant_id"`
	Items        []OrderItem `json:"items"`
	Status       OrderStatus `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

type createOrderReq struct {
	UserID       int64       `json:"user_id"`
	RestaurantID int64       `json:"restaurant_id"`
	Items        []OrderItem `json:"items"`
}

type updateStatusReq struct {
	Status string `json:"status"`
}

type OrderStore struct {
	mu     sync.RWMutex
	orders map[string]*Order
}

func NewOrderStore() *OrderStore {
	return &OrderStore{orders: make(map[string]*Order)}
}

func newOrderID() string {
	return uuid.NewString()
}

func (s *OrderStore) Create(o *Order) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	o.ID = newOrderID()
	now := time.Now()
	o.CreatedAt = now
	o.UpdatedAt = now
	o.Status = StatusPending

	s.orders[o.ID] = o
	return o.ID
}

func (s *OrderStore) Get(id string) (*Order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orders[id]
	return o, ok
}

func (s *OrderStore) UpdateStatus(id string, status OrderStatus) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	o, ok := s.orders[id]
	if !ok {
		return nil, errors.New("order not found")
	}
	o.Status = status
	o.UpdatedAt = time.Now()
	return o, nil
}

func (s *OrderStore) List() []*Order {
	s.mu.RLock()
	defer s.mu.RUnlock()

	orders := make([]*Order, 0, len(s.orders))
	for _, o := range s.orders {
		orders = append(orders, o)
	}
	return orders
}

type orderEvent struct {
	Type  string `json:"type"` // "created" | "status_changed"
	Order *Order `json:"order"`
}

type SSEBroker struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{subs: make(map[chan []byte]struct{})}
}

func (b *SSEBroker) Subscribe() chan []byte {
	ch := make(chan []byte, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *SSEBroker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *SSEBroker) Publish(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Println("publish marshal:", err)
		return
	}
	b.mu.RLock()
	for ch := range b.subs {
		select {
		case ch <- data:
		default:
			// yavaş subscriber'ı bloklamamak için drop ediyoruz
		}
	}
	b.mu.RUnlock()
}

type UpdateStatusMsg struct {
	ID     string
	Status OrderStatus
}

type Server struct {
	App            *fiber.App
	OrderStore     *OrderStore
	UpdateStatusCh chan UpdateStatusMsg
	Broker         *SSEBroker
}

func NewServer(app *fiber.App, os *OrderStore, broker *SSEBroker) *Server {
	return &Server{
		App:            app,
		OrderStore:     os,
		UpdateStatusCh: make(chan UpdateStatusMsg, 256),
		Broker:         broker,
	}
}

func (s *Server) updateStatusWorker() {
	for msg := range s.UpdateStatusCh {
		o, err := s.OrderStore.UpdateStatus(msg.ID, msg.Status)
		if err != nil {
			log.Println(err)
			continue
		}
		log.Printf("Order with id %v is %v!\n", o.ID, o.Status)
		s.Broker.Publish(orderEvent{Type: "status_changed", Order: o})
	}
}

func parseStatus(s string) (OrderStatus, bool) {
	switch OrderStatus(s) {
	case StatusPending, StatusRejected, StatusPreparing, StatusReady:
		return OrderStatus(s), true
	default:
		return "", false
	}
}

func registerRoutes(srv *Server) {
	// Create order
	srv.App.Post("/orders", func(c *fiber.Ctx) error {
		var req createOrderReq
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid body")
		}

		order := &Order{
			UserID:       req.UserID,
			RestaurantID: req.RestaurantID,
			Items:        req.Items,
		}

		orderID := srv.OrderStore.Create(order)
		srv.Broker.Publish(orderEvent{Type: "created", Order: order})

		return c.JSON(fiber.Map{"id": orderID})
	})

	// Get order by id
	srv.App.Get("/orders/:id", func(c *fiber.Ctx) error {
		id := c.Params("id")
		o, ok := srv.OrderStore.Get(id)
		if !ok {
			return fiber.ErrNotFound
		}
		return c.JSON(o)
	})

	// Update order status (by restaurant)
	srv.App.Patch("/restaurants/:rid/orders/:id/status", func(c *fiber.Ctx) error {
		rid, err := strconv.ParseInt(c.Params("rid"), 10, 64)
		if err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid restaurant id")
		}

		id := c.Params("id")
		o, ok := srv.OrderStore.Get(id)
		if !ok {
			return fiber.ErrNotFound
		}
		if o.RestaurantID != rid {
			return fiber.NewError(fiber.StatusForbidden, "order does not belong to this restaurant")
		}

		var req updateStatusReq
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, "invalid body")
		}
		newStatus, ok := parseStatus(req.Status)
		if !ok {
			return fiber.NewError(fiber.StatusBadRequest, "invalid status")
		}
		if o.Status == newStatus {
			return c.JSON(fiber.Map{
				"id":      o.ID,
				"status":  o.Status,
				"message": "status unchanged",
			})
		}

		srv.UpdateStatusCh <- UpdateStatusMsg{ID: id, Status: newStatus}
		return c.JSON(fiber.Map{"id": id, "status": newStatus})
	})

	// SSE stream
	srv.App.Get("/events/orders", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache, no-transform")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")
		c.Set("Access-Control-Allow-Origin", "*")

		sub := srv.Broker.Subscribe()
		ctxDone := c.Context().Done()

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			defer srv.Broker.Unsubscribe(sub)

			// initial snapshot
			if b, err := json.Marshal(srv.OrderStore.List()); err == nil {
				_, _ = w.WriteString("event: snapshot\r\n")
				_, _ = w.WriteString("data: " + string(b) + "\r\n\r\n")
				if err := w.Flush(); err != nil {
					return
				}
			}

			for {
				select {
				case <-ctxDone:
					return
				case msg, ok := <-sub:
					if !ok {
						return
					}
					if _, err := w.WriteString("event: order\r\n"); err != nil {
						return
					}
					if _, err := w.WriteString("data: " + string(msg) + "\r\n\r\n"); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
				}
			}
		})
		return nil
	})
}

func main() {
	orderStore := NewOrderStore()
	sseBroker := NewSSEBroker()

	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowHeaders: "*",
	}))

	srv := NewServer(app, orderStore, sseBroker)

	go srv.updateStatusWorker()

	registerRoutes(srv)

	_ = srv.App.Listen(":8888")
}
