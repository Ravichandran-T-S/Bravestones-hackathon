package message

type WsMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

type StartQuizPayload struct {
	ChannelID string `json:"channelId"`
	HostID    string `json:"hostId"`
}

type QuestionPayload struct {
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	CorrectAnswer int      `json:"correctAnswer"`
	TimerDuration int      `json:"timerDuration"`
	QuestionIndex int      `json:"questionIndex"`
}

type AnswerPayload struct {
	ChannelID     string `json:"channelId"`
	UserID        string `json:"userId"`
	QuestionIndex int    `json:"questionIndex"`
	AnswerIndex   int    `json:"answerIndex"`
}
