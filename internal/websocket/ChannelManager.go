package websocket

import (
	"errors"
	"sync"
	"time"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/message"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ChannelManager manages all active Hubs/Quizzes
type ChannelManager struct {
	db         *gorm.DB
	hubs       map[string]*Hub
	mutex      sync.RWMutex
	maxUsers   int
	quizTimers map[string]*time.Timer // new: track auto-start timers for each quiz
}

// NewChannelManager initializes the manager with DB and a maxUsers limit
func NewChannelManager(db *gorm.DB, maxUsers int) *ChannelManager {
	return &ChannelManager{
		db:         db,
		hubs:       make(map[string]*Hub),
		maxUsers:   maxUsers,
		quizTimers: make(map[string]*time.Timer),
	}
}

// JoinQuiz either creates a new quiz (if none waiting) or joins an existing waiting quiz.
// - If quiz is "active" or "completed", returns an error (no join allowed).
// - If quiz is newly created, sets a 1-minute timer to auto-start.
func (cm *ChannelManager) JoinQuiz(topicName string, user *entity.User) (*Hub, error) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// 1) Find or create the Topic
	var topic entity.Topic
	cm.db.Where("name = ?", topicName).First(&topic)
	if topic.ID == "" {
		topic = entity.Topic{
			ID:        uuid.New().String(),
			Name:      topicName,
			CreatedAt: time.Now(),
		}
		cm.db.Create(&topic)
	}

	// 2) Find an existing quiz (waiting) or create a new one
	var quiz entity.Quiz
	cm.db.Where("topic_id = ? AND status = 'waiting' AND current_users < ?", topic.ID, cm.maxUsers).
		First(&quiz)

	isNew := false
	if quiz.ID == "" {
		isNew = true
		quiz = entity.Quiz{
			ID:           uuid.New().String(),
			TopicID:      topic.ID,
			Status:       "waiting",
			MaxUsers:     cm.maxUsers,
			CurrentUsers: 1,
			CreatedAt:    time.Now(),
		}
		cm.db.Create(&quiz)

		// Create and run new Hub
		hub := NewHub(quiz.ID, cm.db)
		cm.hubs[quiz.ID] = hub
		go hub.Run()
	} else {
		// Check if quiz is still waiting
		if quiz.Status != "waiting" {
			return nil, errors.New("quiz already started or completed")
		}
		quiz.CurrentUsers++
		cm.db.Save(&quiz)
	}

	// 3) Retrieve the hub for this quiz
	hub := cm.hubs[quiz.ID]
	if hub == nil {
		return nil, errors.New("failed to find or create hub")
	}

	// 4) Add user to the Hub's list of joinedUsers (for host rotation, etc.)
	hub.addJoinedUser(user.ID)

	// 5) Create a Score entry if not already present
	var existingScore entity.Score
	cm.db.Where("quiz_id = ? AND user_id = ?", quiz.ID, user.ID).First(&existingScore)
	if existingScore.ID == "" {
		score := entity.Score{
			ID:        uuid.New().String(),
			QuizID:    quiz.ID,
			UserID:    user.ID,
			CreatedAt: time.Now(),
		}
		cm.db.Create(&score)
	}

	// 6) If this is a brand-new quiz, set a 1-minute timer to auto-start the quiz
	if isNew {
		timer := time.AfterFunc(1*time.Minute, func() {
			cm.autoStartQuiz(quiz.ID)
		})
		cm.quizTimers[quiz.ID] = timer
	}

	return hub, nil
}

// autoStartQuiz is triggered by the 1-minute timer if the quiz is still "waiting".
func (cm *ChannelManager) autoStartQuiz(quizID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	var quiz entity.Quiz
	cm.db.First(&quiz, "id = ?", quizID)
	if quiz.ID == "" {
		return
	}
	if quiz.Status != "waiting" {
		return // Quiz already started or was canceled
	}

	// If there's a Hub for this quiz, broadcast a "start_quiz" message with the first user as host
	hub := cm.hubs[quizID]
	if hub == nil {
		return
	}

	// Pick first joined user as host, if any
	var hostID string
	if len(hub.joinedUsers) > 0 {
		hostID = hub.joinedUsers[0]
	}

	quiz.Status = "active"
	quiz.HostID = hostID
	quiz.UpdatedAt = time.Now()
	cm.db.Save(&quiz)

	// Broadcast start_quiz
	hub.broadcast <- message.WsMessage{
		Type: "start_quiz",
		Payload: message.StartQuizPayload{
			ChannelID: quiz.ID,
			HostID:    hostID,
		},
	}
}

// In ChannelManager.go
func (cm *ChannelManager) GetOrCreateHub(quizID string) *Hub {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	hub, ok := cm.hubs[quizID]
	if !ok {
		// Optionally create one if a valid quiz with "waiting" status,
		// or just return nil if no quiz found
		var quiz entity.Quiz
		cm.db.First(&quiz, "id = ?", quizID)
		if quiz.ID == "" {
			return nil
		}
		hub = NewHub(quizID, cm.db)
		cm.hubs[quizID] = hub
		go hub.Run()
	}
	return hub
}
