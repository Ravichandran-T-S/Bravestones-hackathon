package main

import (
	_ "fmt"
	"log"
	"net/http"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/websocket"
	"github.com/gorilla/mux"
	ws "github.com/gorilla/websocket"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Initialize database
	dsn := "host=localhost user=postgres password=postgres dbname=quiz port=5432 sslmode=disable"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Auto-migrate entities
	err = db.AutoMigrate(
		&entity.Quiz{},
		&entity.Score{},
		&entity.User{},
		&entity.Quiz{},
	)
	if err != nil {
		log.Fatal("Auto-migration failed:", err)
	}

	// Create router
	r := mux.NewRouter()

	// WebSocket endpoint
	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(db, w, r)
	})

	// Start server
	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func handleWebSocket(db *gorm.DB, w http.ResponseWriter, r *http.Request) {
	// Get channel ID from query params
	query := r.URL.Query()
	channelID := query.Get("channel")
	if channelID == "" {
		http.Error(w, "Channel ID required", http.StatusBadRequest)
		return
	}

	// Create or get existing hub
	hub := websocket.NewHub(channelID, db)
	go hub.Run()

	// Upgrade to WebSocket connection
	upgrader := ws.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	// Create and register client
	client := &websocket.Client{
		Conn: conn,
		Send: make(chan []byte, 256),
	}
	hub.Register <- client

	// Start communication routines
	go client.WritePump()
	go client.ReadPump(hub)
}
