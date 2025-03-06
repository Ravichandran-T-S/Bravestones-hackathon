package websocket

import (
	"encoding/json"
	"log"
	"time"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/message"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type Hub struct {
	ID         string
	channelID  string
	db         *gorm.DB
	clients    map[*Client]bool
	broadcast  chan message.WsMessage
	Register   chan *Client
	unregister chan *Client
	activeQuiz *QuizSession
}

type Client struct {
	Conn *websocket.Conn
	Send chan []byte
}

type QuizSession struct {
	CurrentQuestion *message.QuestionPayload
	Timer           *time.Timer
	Scores          map[string]int
}

func NewHub(channelID string, db *gorm.DB) *Hub {
	return &Hub{
		ID:         uuid.New().String(),
		channelID:  channelID,
		db:         db,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan message.WsMessage),
		Register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

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

func (h *Hub) handleMessage(msg message.WsMessage) {
	switch msg.Type {
	case "start_quiz":
		payload := msg.Payload.(message.StartQuizPayload)
		h.startQuiz(payload)

	case "submit_answer":
		payload := msg.Payload.(message.AnswerPayload)
		h.handleAnswer(payload)

	case "next_question":
		h.postNextQuestion()
	}
}

func (h *Hub) startQuiz(payload message.StartQuizPayload) {
	// Validate host
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)
	if quiz.HostID != payload.HostID {
		return
	}

	quiz.Status = "active"
	h.db.Save(&quiz)

	h.activeQuiz = &QuizSession{
		Scores: make(map[string]int),
	}

	h.postNextQuestion()
}

func (h *Hub) postNextQuestion() {
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)

	// Get next question (implement your question retrieval logic)
	question := message.QuestionPayload{
		Question:      "What is 2+2?",
		Options:       []string{"3", "4", "5"},
		CorrectAnswer: 1,
		TimerDuration: 30,
		QuestionIndex: quiz.QuestionIndex,
	}

	h.activeQuiz.CurrentQuestion = &question
	quiz.QuestionIndex++
	h.db.Save(&quiz)

	// Broadcast question
	h.broadcastMessage(message.WsMessage{
		Type: "question_posted",
		Payload: struct {
			Question      string   `json:"question"`
			Options       []string `json:"options"`
			TimerDuration int      `json:"timerDuration"`
			QuestionIndex int      `json:"questionIndex"`
		}{
			Question:      question.Question,
			Options:       question.Options,
			TimerDuration: question.TimerDuration,
			QuestionIndex: question.QuestionIndex,
		},
	})

	// Start timer
	h.activeQuiz.Timer = time.AfterFunc(time.Duration(question.TimerDuration)*time.Second, func() {
		h.questionCompleted()
	})
}

func (h *Hub) handleAnswer(payload message.AnswerPayload) {
	if h.activeQuiz == nil ||
		h.activeQuiz.CurrentQuestion == nil ||
		payload.QuestionIndex != h.activeQuiz.CurrentQuestion.QuestionIndex {
		return
	}

	// Calculate score
	var score entity.Score
	h.db.First(&score, "channel_id = ? AND user_id = ?", h.channelID, payload.UserID)

	if payload.AnswerIndex == h.activeQuiz.CurrentQuestion.CorrectAnswer {
		score.TotalScore += 10
		score.LastScore = 10
	} else {
		score.LastScore = 0
	}

	score.LastQuestion = payload.QuestionIndex
	score.QuestionsAsked++
	score.UpdatedAt = time.Now()
	h.db.Save(&score)

	h.activeQuiz.Scores[payload.UserID] = score.TotalScore
}

func (h *Hub) questionCompleted() {
	// Broadcast scores
	var scores []struct {
		UserID    string `json:"userId"`
		Total     int    `json:"total"`
		LastScore int    `json:"lastScore"`
	}

	h.db.Model(&entity.Score{}).Where("channel_id = ?", h.channelID).
		Select("user_id, total_score as total, last_score as last_score").
		Scan(&scores)

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
			QuestionIndex: h.activeQuiz.CurrentQuestion.QuestionIndex,
		},
	})

	// Check if should continue
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)
	if quiz.QuestionIndex >= 10 {
		h.endQuiz()
	}
}

func (h *Hub) endQuiz() {
	var quiz entity.Quiz
	h.db.First(&quiz, "id = ?", h.channelID)
	quiz.Status = "completed"
	h.db.Save(&quiz)

	h.broadcastMessage(message.WsMessage{
		Type: "quiz_ended",
		Payload: struct {
			FinalScores []entity.Score `json:"finalScores"`
		}{
			FinalScores: h.getFinalScores(),
		},
	})

	h.activeQuiz = nil
}

func (h *Hub) getFinalScores() []entity.Score {
	var scores []entity.Score
	h.db.Where("channel_id = ?", h.channelID).Find(&scores)
	return scores
}

func (h *Hub) broadcastMessage(msg message.WsMessage) {
	// Convert message.WsMessage to []byte
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		// Handle error (optional)
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

		var wsMessage message.WsMessage
		if err := json.Unmarshal(msg, &wsMessage); err != nil {
			log.Printf("Message parsing error: %v", err)
			continue
		}

		hub.broadcast <- wsMessage
	}
}

func (c *Client) WritePump() {
	defer c.Conn.Close()

	for {
		message, ok := <-c.Send
		if !ok {
			return
		}

		if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Printf("Write error: %v", err)
			return
		}
	}
}
