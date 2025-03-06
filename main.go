package websocket

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/message"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// Hub manages websocket clients and a single quiz session.
type Hub struct {
	ID         string
	channelID  string // same as quizID
	db         *gorm.DB
	clients    map[*Client]bool
	broadcast  chan message.WsMessage
	Register   chan *Client
	unregister chan *Client

	// Active quiz session data
	activeQuiz *QuizSession

	// Track joined users to handle host rotation
	joinedUsers []string
	mutex       sync.Mutex
}

// Client represents a single WebSocket connection
type Client struct {
	Conn *websocket.Conn
	Send chan []byte
}

// QuizSession holds ephemeral state for the ongoing quiz
type QuizSession struct {
	CurrentQuestion *message.QuestionPayload
	Timer           *time.Timer
	Scores          map[string]int
	HostIndex       int // which user in joinedUsers is the current host
}

// NewHub creates a Hub for a given quizID
func NewHub(channelID string, db *gorm.DB) *Hub {
	return &Hub{
		ID:         uuid.New().String(),
		channelID:  channelID,
		db:         db,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan message.WsMessage),
		Register:   make(chan *Client),
		unregister: make(chan *Client),
		activeQuiz: nil,
	}
}

// Run processes register/unregister and broadcasts
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.clients[client] = true
		case client := <-h.unregister:
			delete(h.clients, client)
			close(client.Send)
		case msg := <-h.broadcast:
			h.handleMessage(msg)
		}
	}
}

// addJoinedUser is called from ChannelManager.JoinQuiz to track who joined (for host rotation).
func (h *Hub) addJoinedUser(userID string) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	// Prevent duplicates
	for _, uid := range h.joinedUsers {
		if uid == userID {
			return
		}
	}
	h.joinedUsers = append(h.joinedUsers, userID)
}

// handleMessage routes incoming WsMessage by type
func (h *Hub) handleMessage(msg message.WsMessage) {
	switch msg.Type {

	case "start_quiz":
		// Usually called automatically from ChannelManager.
		// But if triggered by a user, you could handle logic here.
		pl, ok := msg.Payload.(map[string]interface{})
		if !ok {
			return
		}
		channelID := pl["channelId"].(string)
		hostID := pl["hostId"].(string)
		h.startQuiz(channelID, hostID)

	case "question_posted":
		// Host is posting a new question
		h.handleQuestionPosted(msg.Payload)

	case "submit_answer":
		// Participant is submitting an answer
		h.handleAnswer(msg.Payload)

	case "end_question":
		// Host forcibly ends the question timer
		h.questionCompleted()

	default:
		log.Println("Unknown message type:", msg.Type)
	}
}

// startQuiz sets quiz status to "active", picks/validates host, initializes session
func (h *Hub) startQuiz(quizID, hostID string) {
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", quizID)
	if quiz.ID == "" {
		return
	}

	// If the quiz is still waiting, or forcibly re-starting:
	if quiz.Status == "waiting" {
		quiz.Status = "active"
		quiz.HostID = hostID
		quiz.UpdatedAt = time.Now()
		h.db.Save(&quiz)
	}

	// Initialize session if not already
	if h.activeQuiz == nil {
		h.activeQuiz = &QuizSession{
			Scores:    make(map[string]int),
			HostIndex: 0,
		}
	}

	// Broadcast to all clients that the quiz is started
	h.broadcastMessage(message.WsMessage{
		Type: "quiz_started",
		Payload: message.StartQuizPayload{
			ChannelID: quizID,
			HostID:    hostID,
		},
	})
}

// handleQuestionPosted starts the question timer (30s default or from payload)
func (h *Hub) handleQuestionPosted(payload interface{}) {
	var qp message.QuestionPayload
	bytes, _ := json.Marshal(payload)
	if err := json.Unmarshal(bytes, &qp); err != nil {
		log.Println("Invalid QuestionPayload:", err)
		return
	}

	// Validate host: only current host can post a question
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)

	if quiz.ID == "" || quiz.Status != "active" {
		return
	}
	// If you want to verify that the user sending "question_posted" is quiz.HostID,
	// you'll need to pass user ID along. For brevity, assume okay.

	// Save question in session
	if h.activeQuiz == nil {
		h.activeQuiz = &QuizSession{
			Scores: make(map[string]int),
		}
	}

	h.activeQuiz.CurrentQuestion = &qp

	// Increment the quiz.QuestionIndex
	quiz.QuestionIndex++
	h.db.Save(&quiz)

	// The host is about to ask a question -> increment "QuestionsAsked"
	var hostScore entity.Score
	h.db.Where("quiz_id = ? AND user_id = ?", quiz.ID, quiz.HostID).First(&hostScore)
	if hostScore.ID != "" {
		hostScore.QuestionsAsked++
		hostScore.UpdatedAt = time.Now()
		h.db.Save(&hostScore)
	}

	// Broadcast question to everyone
	h.broadcastMessage(message.WsMessage{
		Type: "question_posted",
		Payload: message.QuestionPayload{
			Question:      qp.Question,
			Options:       qp.Options,
			TimerDuration: qp.TimerDuration,
			QuestionIndex: quiz.QuestionIndex,
			// Do not reveal CorrectAnswer in broadcast
		},
	})

	// Start a 30-second timer (or custom)
	duration := 30
	if qp.TimerDuration > 0 {
		duration = qp.TimerDuration
	}
	if h.activeQuiz.Timer != nil {
		h.activeQuiz.Timer.Stop()
	}
	h.activeQuiz.Timer = time.AfterFunc(time.Duration(duration)*time.Second, func() {
		h.questionCompleted()
	})
}

// handleAnswer updates a user's score
func (h *Hub) handleAnswer(payload interface{}) {
	var ans message.AnswerPayload
	bytes, _ := json.Marshal(payload)
	if err := json.Unmarshal(bytes, &ans); err != nil {
		log.Println("Invalid AnswerPayload:", err)
		return
	}

	// Validate we have a current question
	if h.activeQuiz == nil || h.activeQuiz.CurrentQuestion == nil {
		return
	}
	// Validate question index
	if ans.QuestionIndex != h.activeQuiz.CurrentQuestion.QuestionIndex {
		return
	}

	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", ans.ChannelID)
	if quiz.ID == "" {
		return
	}

	// Check if answer is correct
	isCorrect := (ans.AnswerIndex == h.activeQuiz.CurrentQuestion.CorrectAnswer)

	// Update Score table
	var score entity.Score
	h.db.Where("quiz_id = ? AND user_id = ?", ans.ChannelID, ans.UserID).First(&score)

	if isCorrect {
		score.TotalScore += 10
		score.LastScore = 10
	} else {
		score.LastScore = 0
	}
	score.LastQuestion = ans.QuestionIndex
	score.UpdatedAt = time.Now()
	h.db.Save(&score)

	// Track ephemeral score, too
	h.activeQuiz.Scores[ans.UserID] = score.TotalScore
}

// questionCompleted is called when the timer ends or host forcibly ends question
func (h *Hub) questionCompleted() {
	// If no active session or no current question, do nothing
	if h.activeQuiz == nil || h.activeQuiz.CurrentQuestion == nil {
		return
	}
	// Stop timer if still running
	if h.activeQuiz.Timer != nil {
		h.activeQuiz.Timer.Stop()
		h.activeQuiz.Timer = nil
	}

	// Gather scores from DB for the scoreboard
	var scores []struct {
		UserID    string `json:"userId"`
		Total     int    `json:"total"`
		LastScore int    `json:"lastScore"`
	}
	h.db.Model(&entity.Score{}).
		Where("quiz_id = ?", h.channelID).
		Select("user_id, total_score as total, last_score as last_score").
		Scan(&scores)

	// Broadcast "question_completed"
	qIndex := h.activeQuiz.CurrentQuestion.QuestionIndex
	h.broadcastMessage(message.WsMessage{
		Type: "question_completed",
		Payload: struct {
			Scores []struct {
				UserID    string `json:"userId"`
				Total     int    `json:"total"`
				LastScore int    `json:"lastScore"`
			} `json:"scores"`
			QuestionIndex int `json:"questionIndex"`
		}{
			Scores:        scores,
			QuestionIndex: qIndex,
		},
	})

	// Rotate host or end quiz
	h.rotateHostOrEnd()
	// Clear current question
	h.activeQuiz.CurrentQuestion = nil
}

// rotateHostOrEnd either moves to the next host or ends quiz if everyone has hosted
func (h *Hub) rotateHostOrEnd() {
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)
	if quiz.ID == "" {
		return
	}

	// Find current host index in h.joinedUsers
	currHostIndex := -1
	for i, uid := range h.joinedUsers {
		if uid == quiz.HostID {
			currHostIndex = i
			break
		}
	}
	if currHostIndex == -1 {
		// fallback
		currHostIndex = 0
	}

	// Move to next
	nextIndex := currHostIndex + 1
	if nextIndex >= len(h.joinedUsers) {
		// Everyone has hosted once -> end quiz
		h.endQuiz()
		return
	}

	// Otherwise, new host
	newHost := h.joinedUsers[nextIndex]
	quiz.HostID = newHost
	quiz.UpdatedAt = time.Now()
	h.db.Save(&quiz)

	// Broadcast updated host
	h.broadcastMessage(message.WsMessage{
		Type: "new_host",
		Payload: struct {
			NewHostID string `json:"newHostId"`
		}{
			NewHostID: newHost,
		},
	})

	// Now wait for the next "question_posted" from the new host
}

// endQuiz sets quiz status to "completed" and broadcasts final scores
func (h *Hub) endQuiz() {
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)
	if quiz.ID == "" {
		return
	}
	quiz.Status = "completed"
	quiz.UpdatedAt = time.Now()
	h.db.Save(&quiz)

	// Final scores
	finalScores := h.getFinalScores()

	h.broadcastMessage(message.WsMessage{
		Type: "quiz_ended",
		Payload: struct {
			FinalScores []entity.Score `json:"finalScores"`
		}{
			FinalScores: finalScores,
		},
	})

	// Clear active quiz from memory
	h.activeQuiz = nil
}

// getFinalScores returns all Score rows for this quiz
func (h *Hub) getFinalScores() []entity.Score {
	var scores []entity.Score
	h.db.Where("quiz_id = ?", h.channelID).Find(&scores)
	return scores
}

// broadcastMessage sends a WsMessage to all connected clients
func (h *Hub) broadcastMessage(msg message.WsMessage) {
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Println("Failed to marshal broadcast:", err)
		return
	}

	for client := range h.clients {
		select {
		case client.Send <- msgBytes:
		default:
			close(client.Send)
			delete(h.clients, client)
		}
	}
}

// Client Read/Write pumps

func (c *Client) ReadPump(hub *Hub) {
	defer func() {
		hub.unregister <- c
		c.Conn.Close()
	}()

	for {
		_, msg, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Read error: %v", err)
			}
			break
		}

		var wsMsg message.WsMessage
		if err := json.Unmarshal(msg, &wsMsg); err != nil {
			log.Printf("Message parsing error: %v", err)
			continue
		}

		// Send to Hub for processing
		hub.broadcast <- wsMsg
	}
}

func (c *Client) WritePump() {
	defer c.Conn.Close()

	for {
		select {
		case message, ok := <-c.Send:
			if !ok {
				// Hub closed channel
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("Write error: %v", err)
				return
			}
		}
	}
}
