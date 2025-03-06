package main

import (
	"log"
	"net/http"
	"time"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/websocket"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	ws "github.com/gorilla/websocket"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	channelManager *websocket.ChannelManager
	db             *gorm.DB
)

func main() {
	// Initialize database
	dsn := "host=localhost user=postgres password=postgres dbname=quiz port=5432 sslmode=disable"
	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Auto-migrate
	err = db.AutoMigrate(
		&entity.Quiz{},
		&entity.Score{},
		&entity.User{},
		&entity.Topic{}, // ensure Topic is migrated too
	)
	if err != nil {
		log.Fatal("Auto-migration failed:", err)
	}

	// Initialize ChannelManager
	channelManager = websocket.NewChannelManager(db, 10) // maxUsers = 10 (example)

	r := mux.NewRouter()

	// Example: minimal /join endpoint
	r.HandleFunc("/join", handleJoin).Methods("POST")

	// WebSocket endpoint
	r.HandleFunc("/ws", handleWebSocket)

	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

// handleJoin is a simple example of an HTTP join API
func handleJoin(w http.ResponseWriter, r *http.Request) {
	// Parse form or JSON to get topic + username
	topicName := r.FormValue("topic")
	username := r.FormValue("username")
	if topicName == "" || username == "" {
		http.Error(w, "Missing topic or username", http.StatusBadRequest)
		return
	}

	// Check if user exists or create
	var user entity.User
	db.Where("username = ?", username).First(&user)
	if user.ID == "" {
		user.ID = uuid.New().String()
		user.Username = username
		user.CreatedAt = time.Now()
		db.Create(&user)
	}

	_, err := channelManager.JoinQuiz(topicName, &user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	w.Write([]byte("Joined quiz successfully"))
}

// handleWebSocket upgrades the connection and registers the client to the relevant Hub
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// quizID is your "channel"
	quizID := r.URL.Query().Get("channel")
	if quizID == "" {
		http.Error(w, "Channel (quizID) required", http.StatusBadRequest)
		return
	}

	// If there's no existing hub, create one (or handle error)
	hub := channelManager.GetOrCreateHub(quizID) // You can implement a helper
	if hub == nil {
		http.Error(w, "No hub available", http.StatusNotFound)
		return
	}

	upgrader := ws.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &websocket.Client{
		Conn: conn,
		Send: make(chan []byte, 256),
	}
	hub.Register <- client

	go client.WritePump()
	go client.ReadPump(hub)
}
