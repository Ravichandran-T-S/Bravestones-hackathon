package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Upgrader is used to upgrade HTTP connections to WebSocket connections.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all connections (for demonstration purposes)
	},
}

// Define a struct to represent the JSON message
type Message struct {
	Signal string            `json:"signal"` // Signal type (e.g., "create_user")
	Data   map[string]string `json:"data"`   // Data associated with the signal
}

// Client represents a connected WebSocket client
type Client struct {
	conn *websocket.Conn
	send chan []byte
}

// Hub manages all connected clients
type Hub struct {
	clients    map[*Client]bool  // Track all connected clients
	broadcast  chan []byte       // Channel for broadcasting messages to all clients
	register   chan *Client      // Channel for registering new clients
	unregister chan *Client      // Channel for unregistering clients
	users      map[string]string // Shared state: map of users (name -> email)
	mu         sync.Mutex        // Mutex to protect shared state
}

// NewHub creates a new Hub instance
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		users:      make(map[string]string),
	}
}

// Run starts the Hub
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			// Register a new client
			h.clients[client] = true
			fmt.Println("Client connected")

		case client := <-h.unregister:
			// Unregister a client
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				fmt.Println("Client disconnected")
			}

		case message := <-h.broadcast:
			// Broadcast a message to all clients
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// handleWebSocket handles WebSocket connections
func (h *Hub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	// Create a new client
	client := &Client{
		conn: conn,
		send: make(chan []byte, 256),
	}

	// Register the client with the Hub
	h.register <- client

	// Start a goroutine to read messages from the client
	go func() {
		defer func() {
			h.unregister <- client
			conn.Close()
		}()

		for {
			// Read the message from the WebSocket connection
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("Read error:", err)
				break
			}

			// Log the received message
			fmt.Printf("Received: %s\n", message)

			// Parse the JSON message into the Message struct
			var msg Message
			if err := json.Unmarshal(message, &msg); err != nil {
				log.Println("JSON unmarshal error:", err)
				conn.WriteMessage(websocket.TextMessage, []byte(`{"error": "Invalid JSON"}`))
				continue
			}

			// Handle the signal
			switch msg.Signal {
			case "create_user":
				// Extract user data from the message
				name, ok := msg.Data["name"]
				if !ok {
					conn.WriteMessage(websocket.TextMessage, []byte(`{"error": "Missing 'name' field"}`))
					continue
				}
				email, ok := msg.Data["email"]
				if !ok {
					conn.WriteMessage(websocket.TextMessage, []byte(`{"error": "Missing 'email' field"}`))
					continue
				}

				// Add the user to the shared state
				h.mu.Lock()
				h.users[name] = email
				h.mu.Unlock()

				// Broadcast the new user to all clients
				response := map[string]interface{}{
					"signal": "user_created",
					"data": map[string]string{
						"name":  name,
						"email": email,
					},
				}
				responseJSON, err := json.Marshal(response)
				if err != nil {
					log.Println("JSON marshal error:", err)
					continue
				}
				h.broadcast <- responseJSON

			default:
				// Handle unknown signals
				conn.WriteMessage(websocket.TextMessage, []byte(`{"error": "Unknown signal"}`))
			}
		}
	}()

	// Start a goroutine to write messages to the client
	go func() {
		defer func() {
			conn.Close()
		}()

		for {
			select {
			case message, ok := <-client.send:
				if !ok {
					// The Hub closed the channel
					conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
					log.Println("Write error:", err)
					return
				}
			}
		}
	}()
}

func main() {
	// Create a new Hub
	hub := NewHub()
	go hub.Run()

	// Register the WebSocket handler
	http.HandleFunc("/ws", hub.handleWebSocket)

	fmt.Println("WebSocket server started at ws://localhost:8080/ws")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
