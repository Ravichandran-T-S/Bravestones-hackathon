package websocket

import (
	"sync"
	"time"

	"github.com/Ravichandran-T-S/Bravestones-hackathon/internal/data/entity"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ChannelManager struct {
	db       *gorm.DB
	hubs     map[string]*Hub
	mutex    sync.RWMutex
	maxUsers int
}

func NewChannelManager(db *gorm.DB, maxUsers int) *ChannelManager {
	return &ChannelManager{
		db:       db,
		hubs:     make(map[string]*Hub),
		maxUsers: maxUsers,
	}
}

func (cm *ChannelManager) JoinQuiz(topicName string, user *entity.User) (*Hub, error) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Find or create topic
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

	// Find available quiz
	var quiz entity.Quiz
	cm.db.Where("topic_id = ? AND status = 'waiting' AND current_users < ?",
		topic.ID, cm.maxUsers).First(&quiz)

	if quiz.ID == "" {
		quiz = entity.Quiz{
			ID:           uuid.New().String(),
			TopicID:      topic.ID,
			Status:       "waiting",
			MaxUsers:     cm.maxUsers,
			CurrentUsers: 1,
			CreatedAt:    time.Now(),
		}
		cm.db.Create(&quiz)

		hub := NewHub(quiz.ID, cm.db)
		cm.hubs[quiz.ID] = hub
		go hub.Run()
	} else {
		quiz.CurrentUsers++
		cm.db.Save(&quiz)
	}

	// Create user score entry
	score := entity.Score{
		ID:        uuid.New().String(),
		QuizID:    quiz.ID,
		UserID:    user.ID,
		CreatedAt: time.Now(),
	}
	cm.db.Create(&score)

	return cm.hubs[quiz.ID], nil
}
