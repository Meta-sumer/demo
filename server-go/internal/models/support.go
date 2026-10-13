package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// Live support chat.
//
// One conversation per customer, reused for life; a resolved conversation
// reopens the moment the customer writes again. Read receipts are two
// timestamps on the conversation rather than a flag per message - a message has
// been read by the other side when it was created no later than that side's
// readAt, so marking a thread read is one write however long it is.
const (
	CollSupportConversations = "support_conversations"
	CollSupportMessages      = "support_messages"
)

type SupportStatus string

const (
	SupportStatusOpen     SupportStatus = "OPEN"
	SupportStatusResolved SupportStatus = "RESOLVED"
)

var SupportStatusValues = []string{string(SupportStatusOpen), string(SupportStatusResolved)}

type SupportSender string

const (
	SupportSenderUser   SupportSender = "USER"
	SupportSenderAgent  SupportSender = "AGENT"
	SupportSenderSystem SupportSender = "SYSTEM"
)

var SupportSenderValues = []string{string(SupportSenderUser), string(SupportSenderAgent), string(SupportSenderSystem)}

// SupportMessageMaxLength is the schema maxlength on a message body.
const SupportMessageMaxLength = 2000

type SupportConversation struct {
	ID                 bson.ObjectID  `bson:"_id,omitempty"`
	UserID             bson.ObjectID  `bson:"userId"`
	Status             SupportStatus  `bson:"status"`
	AssignedTo         *bson.ObjectID `bson:"assignedTo"`
	AssignedName       *string        `bson:"assignedName"`
	LastMessageAt      *time.Time     `bson:"lastMessageAt"`
	LastMessagePreview *string        `bson:"lastMessagePreview"`
	LastSender         *SupportSender `bson:"lastSender"`
	// Agent messages the customer has not seen yet.
	UnreadByUser int64 `bson:"unreadByUser"`
	// Customer messages the desk has not seen yet.
	UnreadByAgent int64      `bson:"unreadByAgent"`
	UserReadAt    *time.Time `bson:"userReadAt"`
	AgentReadAt   *time.Time `bson:"agentReadAt"`
	CreatedAt     time.Time  `bson:"createdAt"`
	UpdatedAt     time.Time  `bson:"updatedAt"`
	V             int64      `bson:"__v"`
}

// SupportMessage: timestamps { createdAt only }.
type SupportMessage struct {
	ID             bson.ObjectID  `bson:"_id,omitempty"`
	ConversationID bson.ObjectID  `bson:"conversationId"`
	Sender         SupportSender  `bson:"sender"`
	SenderID       *bson.ObjectID `bson:"senderId"`
	SenderName     string         `bson:"senderName"`
	Body           string         `bson:"body"`
	CreatedAt      time.Time      `bson:"createdAt"`
	V              int64          `bson:"__v"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollSupportConversations, Keys: bson.D{{Key: "userId", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollSupportConversations, Keys: bson.D{{Key: "status", Value: 1}, {Key: "lastMessageAt", Value: -1}}},
		mongox.Index{Collection: CollSupportMessages, Keys: bson.D{{Key: "conversationId", Value: 1}, {Key: "createdAt", Value: -1}}},
	)
}

type SupportUserSummary struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	Username string  `json:"username"`
	FullName *string `json:"fullName"`
}

type ConversationView struct {
	ID                 string              `json:"id"`
	UserID             string              `json:"userId"`
	User               *SupportUserSummary `json:"user,omitempty"`
	Status             SupportStatus       `json:"status"`
	AssignedTo         *string             `json:"assignedTo"`
	AssignedName       *string             `json:"assignedName"`
	LastMessageAt      *iso.Time           `json:"lastMessageAt"`
	LastMessagePreview *string             `json:"lastMessagePreview"`
	LastSender         *SupportSender      `json:"lastSender"`
	UnreadByUser       int64               `json:"unreadByUser"`
	UnreadByAgent      int64               `json:"unreadByAgent"`
	UserReadAt         *iso.Time           `json:"userReadAt"`
	AgentReadAt        *iso.Time           `json:"agentReadAt"`
	CreatedAt          iso.Time            `json:"createdAt"`
}

// ToConversationView: user is attached for the desk only (nil omits the key).
func ToConversationView(c *SupportConversation, user *SupportUserSummary) ConversationView {
	return ConversationView{
		ID:                 c.ID.Hex(),
		UserID:             c.UserID.Hex(),
		User:               user,
		Status:             c.Status,
		AssignedTo:         hexPtr(c.AssignedTo),
		AssignedName:       c.AssignedName,
		LastMessageAt:      iso.Ptr(c.LastMessageAt),
		LastMessagePreview: c.LastMessagePreview,
		LastSender:         c.LastSender,
		UnreadByUser:       c.UnreadByUser,
		UnreadByAgent:      c.UnreadByAgent,
		UserReadAt:         iso.Ptr(c.UserReadAt),
		AgentReadAt:        iso.Ptr(c.AgentReadAt),
		CreatedAt:          iso.From(c.CreatedAt),
	}
}

type MessageView struct {
	ID             string        `json:"id"`
	ConversationID string        `json:"conversationId"`
	Sender         SupportSender `json:"sender"`
	SenderName     string        `json:"senderName"`
	Body           string        `json:"body"`
	CreatedAt      iso.Time      `json:"createdAt"`
}

func ToMessageView(m *SupportMessage) MessageView {
	return MessageView{
		ID:             m.ID.Hex(),
		ConversationID: m.ConversationID.Hex(),
		Sender:         m.Sender,
		SenderName:     m.SenderName,
		Body:           m.Body,
		CreatedAt:      iso.From(m.CreatedAt),
	}
}
