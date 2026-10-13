// Package support is the live support desk.
//
// Every write here ends in a websocket event to both sides - the customer on
// their own `user:` channel, the desk on `admin:support` - so neither side
// ever has to poll. The database is still the record: a client that missed
// events while offline reloads the thread and is exactly up to date.
package support

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/ws"
)

const (
	previewLength = 140
	maxPage       = 100
)

func conversations() *mongo.Collection { return mongox.C(models.CollSupportConversations) }
func messages() *mongo.Collection      { return mongox.C(models.CollSupportMessages) }

type userNames struct {
	ID       bson.ObjectID `bson:"_id"`
	Email    string        `bson:"email"`
	Username string        `bson:"username"`
	FullName *string       `bson:"fullName"`
}

func findUser(ctx context.Context, id bson.ObjectID) (*userNames, error) {
	var u userNames
	err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{"email": 1, "username": 1, "fullName": 1})).Decode(&u)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func summaryOf(u *userNames) *models.SupportUserSummary {
	return &models.SupportUserSummary{ID: u.ID.Hex(), Email: u.Email, Username: u.Username, FullName: u.FullName}
}

func userSummary(ctx context.Context, userID bson.ObjectID) (*models.SupportUserSummary, error) {
	u, err := findUser(ctx, userID)
	if err != nil || u == nil {
		return nil, err
	}
	return summaryOf(u), nil
}

// jsTrim approximates String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
}

// displayName is `fullName?.trim() || username || fallback`.
func displayName(u *userNames, fallback string) string {
	if u == nil {
		return fallback
	}
	if u.FullName != nil {
		if n := jsTrim(*u.FullName); n != "" {
			return n
		}
	}
	if u.Username != "" {
		return u.Username
	}
	return fallback
}

// AgentDisplayName is the name the other side sees for an agent - their name,
// never their email.
func AgentDisplayName(ctx context.Context, agentID string) (string, error) {
	id, ok := mongox.ParseID(agentID)
	if !ok {
		return "Support", nil
	}
	u, err := findUser(ctx, id)
	if err != nil {
		return "", err
	}
	return displayName(u, "Support"), nil
}

// broadcastConversation tells both sides the conversation changed (status,
// unread counts, receipts) and returns the desk view.
func broadcastConversation(ctx context.Context, doc *models.SupportConversation) (models.ConversationView, error) {
	forUser := models.ToConversationView(doc, nil)
	ws.Emit([]string{config.ChUser(doc.UserID.Hex())}, config.EvSupportConversation, forUser)
	user, err := userSummary(ctx, doc.UserID)
	if err != nil {
		return models.ConversationView{}, err
	}
	forDesk := models.ToConversationView(doc, user)
	ws.Emit([]string{config.ChAdminSupport}, config.EvSupportConversation, forDesk)
	return forDesk, nil
}

// ── customer ────────────────────────────────────────────────────────────

// GetOrCreateConversation returns the customer's one conversation, created on
// first visit.
func GetOrCreateConversation(ctx context.Context, userID string) (*models.SupportConversation, error) {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return nil, err
	}
	now := mongox.Now()
	var doc models.SupportConversation
	err = conversations().FindOneAndUpdate(ctx,
		bson.M{"userId": uid},
		bson.M{
			"$setOnInsert": bson.M{
				"status":             models.SupportStatusOpen,
				"assignedTo":         nil,
				"assignedName":       nil,
				"lastMessageAt":      nil,
				"lastMessagePreview": nil,
				"lastSender":         nil,
				"unreadByUser":       int64(0),
				"unreadByAgent":      int64(0),
				"userReadAt":         nil,
				"agentReadAt":        nil,
				"createdAt":          now,
			},
			"$set": bson.M{"updatedAt": now},
		},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// GetConversationView is the customer's view of their conversation.
func GetConversationView(ctx context.Context, userID string) (models.ConversationView, error) {
	doc, err := GetOrCreateConversation(ctx, userID)
	if err != nil {
		return models.ConversationView{}, err
	}
	return models.ToConversationView(doc, nil), nil
}

func getByID(ctx context.Context, conversationID string) (*models.SupportConversation, error) {
	id, ok := mongox.ParseID(conversationID)
	if !ok {
		return nil, apperr.NotFoundErr("", "")
	}
	var doc models.SupportConversation
	if err := conversations().FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.NotFoundErr("", "")
		}
		return nil, err
	}
	return &doc, nil
}

// ListMessagesOptions: Before is a message id; Limit nil = 50.
type ListMessagesOptions struct {
	Before *string
	Limit  *int64
}

// MessagePage is one page of a thread, oldest first.
type MessagePage struct {
	Items   []models.MessageView `json:"items"`
	HasMore bool                 `json:"hasMore"`
}

// ListMessages returns one page of messages, newest page first, oldest-first
// for display. Pass the oldest id on screen as Before to load further back.
func ListMessages(ctx context.Context, conversationID string, opts ListMessagesOptions) (MessagePage, error) {
	limit := int64(50)
	if opts.Limit != nil {
		limit = *opts.Limit
	}
	limit = min(max(limit, 1), maxPage)
	cid, err := bson.ObjectIDFromHex(conversationID)
	if err != nil {
		return MessagePage{}, err
	}
	filter := bson.M{"conversationId": cid}
	if opts.Before != nil && *opts.Before != "" {
		if bid, ok := mongox.ParseID(*opts.Before); ok {
			var anchor struct {
				CreatedAt bson.DateTime `bson:"createdAt"`
			}
			err := messages().FindOne(ctx, bson.M{"_id": bid},
				options.FindOne().SetProjection(bson.M{"createdAt": 1})).Decode(&anchor)
			if err == nil {
				filter["createdAt"] = bson.M{"$lt": anchor.CreatedAt}
			} else if !mongox.IsNotFound(err) {
				return MessagePage{}, err
			}
		}
	}
	cur, err := messages().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetLimit(limit+1))
	if err != nil {
		return MessagePage{}, err
	}
	var rows []models.SupportMessage
	if err := cur.All(ctx, &rows); err != nil {
		return MessagePage{}, err
	}
	hasMore := int64(len(rows)) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]models.MessageView, len(rows))
	for i := range rows {
		items[len(rows)-1-i] = models.ToMessageView(&rows[i])
	}
	return MessagePage{Items: items, HasMore: hasMore}, nil
}

// ── messaging ───────────────────────────────────────────────────────────

// PostResult is what posting a message returns.
type PostResult struct {
	Message      models.MessageView      `json:"message"`
	Conversation models.ConversationView `json:"conversation"`
}

type postInput struct {
	conversation *models.SupportConversation
	sender       models.SupportSender
	senderID     string
	senderName   string
	body         string
}

// jsLen is String.prototype.length (UTF-16 code units).
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// preview mirrors `body.length > 140 ? body.slice(0, 139) + '…' : body`,
// slicing UTF-16 units as JavaScript does.
func preview(body string) string {
	units := utf16.Encode([]rune(body))
	if len(units) <= previewLength {
		return body
	}
	return string(utf16.Decode(units[:previewLength-1])) + "…"
}

// validateBody trims and checks a message body.
func validateBody(raw string) (string, error) {
	body := jsTrim(raw)
	if body == "" {
		return "", apperr.Validation("Message is empty", nil)
	}
	if jsLen(body) > models.SupportMessageMaxLength {
		return "", apperr.Validation("Message is longer than 2000 characters", nil)
	}
	return body, nil
}

func post(ctx context.Context, in postInput) (PostResult, error) {
	body, err := validateBody(in.body)
	if err != nil {
		return PostResult{}, err
	}

	msg := models.SupportMessage{
		ConversationID: in.conversation.ID,
		Sender:         in.sender,
		SenderName:     in.senderName,
		Body:           body,
		CreatedAt:      mongox.Now(),
	}
	if id, ok := mongox.ParseID(in.senderID); ok {
		msg.SenderID = &id
	}
	res, err := messages().InsertOne(ctx, msg)
	if err != nil {
		return PostResult{}, err
	}
	msg.ID, _ = res.InsertedID.(bson.ObjectID)
	message := models.ToMessageView(&msg)

	now := msg.CreatedAt
	set := bson.M{
		"lastMessageAt":      now,
		"lastMessagePreview": preview(body),
		"lastSender":         in.sender,
		"updatedAt":          mongox.Now(),
	}
	inc := bson.M{}
	switch in.sender {
	case models.SupportSenderUser:
		// Writing again reopens a resolved conversation, and the customer has by
		// definition seen everything up to their own message.
		set["status"] = models.SupportStatusOpen
		set["userReadAt"] = now
		set["unreadByUser"] = int64(0)
		inc["unreadByAgent"] = int64(1)
	case models.SupportSenderAgent:
		set["agentReadAt"] = now
		set["unreadByAgent"] = int64(0)
		inc["unreadByUser"] = int64(1)
		// The first agent to answer an unassigned conversation owns it.
		if in.conversation.AssignedTo == nil && msg.SenderID != nil {
			set["assignedTo"] = *msg.SenderID
			set["assignedName"] = in.senderName
		}
	}
	update := bson.M{"$set": set}
	if len(inc) > 0 {
		update["$inc"] = inc
	}
	var updated models.SupportConversation
	err = conversations().FindOneAndUpdate(ctx, bson.M{"_id": in.conversation.ID}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		return PostResult{}, err
	}

	ws.Emit([]string{config.ChUser(updated.UserID.Hex()), config.ChAdminSupport}, config.EvSupportMessage, message)
	conversation, err := broadcastConversation(ctx, &updated)
	if err != nil {
		return PostResult{}, err
	}
	return PostResult{Message: message, Conversation: conversation}, nil
}

// PostCustomerMessage adds the customer's message to their conversation.
func PostCustomerMessage(ctx context.Context, userID, body string) (PostResult, error) {
	conversation, err := GetOrCreateConversation(ctx, userID)
	if err != nil {
		return PostResult{}, err
	}
	u, err := findUser(ctx, conversation.UserID)
	if err != nil {
		return PostResult{}, err
	}
	return post(ctx, postInput{
		conversation: conversation,
		sender:       models.SupportSenderUser,
		senderID:     userID,
		senderName:   displayName(u, "Customer"),
		body:         body,
	})
}

// PostAgentMessage adds an agent's reply.
func PostAgentMessage(ctx context.Context, conversationID, agentID, body string) (PostResult, error) {
	conversation, err := getByID(ctx, conversationID)
	if err != nil {
		return PostResult{}, err
	}
	name, err := AgentDisplayName(ctx, agentID)
	if err != nil {
		return PostResult{}, err
	}
	return post(ctx, postInput{
		conversation: conversation,
		sender:       models.SupportSenderAgent,
		senderID:     agentID,
		senderName:   name,
		body:         body,
	})
}

// ── receipts & typing ───────────────────────────────────────────────────

func setAndReturn(ctx context.Context, id bson.ObjectID, set bson.M) (*models.SupportConversation, error) {
	set["updatedAt"] = mongox.Now()
	var updated models.SupportConversation
	err := conversations().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// MarkReadByUser records the customer's read receipt; returns their view.
func MarkReadByUser(ctx context.Context, userID string) (models.ConversationView, error) {
	conversation, err := GetOrCreateConversation(ctx, userID)
	if err != nil {
		return models.ConversationView{}, err
	}
	updated, err := setAndReturn(ctx, conversation.ID, bson.M{"userReadAt": mongox.Now(), "unreadByUser": int64(0)})
	if err != nil {
		return models.ConversationView{}, err
	}
	if _, err := broadcastConversation(ctx, updated); err != nil {
		return models.ConversationView{}, err
	}
	return models.ToConversationView(updated, nil), nil
}

// MarkReadByAgent records the desk's read receipt; returns the desk view.
func MarkReadByAgent(ctx context.Context, conversationID string) (models.ConversationView, error) {
	conversation, err := getByID(ctx, conversationID)
	if err != nil {
		return models.ConversationView{}, err
	}
	updated, err := setAndReturn(ctx, conversation.ID, bson.M{"agentReadAt": mongox.Now(), "unreadByAgent": int64(0)})
	if err != nil {
		return models.ConversationView{}, err
	}
	return broadcastConversation(ctx, updated)
}

// TypingSignal is the support.typing payload. Name is sent for agents only.
type TypingSignal struct {
	ConversationID string `json:"conversationId"`
	Side           string `json:"side"`
	Name           string `json:"name,omitempty"`
}

// SignalTypingByUser: "someone is typing" goes to the other side only and is
// never stored - it is worth nothing a few seconds later.
func SignalTypingByUser(ctx context.Context, userID string) error {
	conversation, err := GetOrCreateConversation(ctx, userID)
	if err != nil {
		return err
	}
	ws.Emit([]string{config.ChAdminSupport}, config.EvSupportTyping,
		TypingSignal{ConversationID: conversation.ID.Hex(), Side: string(models.SupportSenderUser)})
	return nil
}

// SignalTypingByAgent tells the customer an agent is typing.
func SignalTypingByAgent(ctx context.Context, conversationID, agentID string) error {
	conversation, err := getByID(ctx, conversationID)
	if err != nil {
		return err
	}
	name, err := AgentDisplayName(ctx, agentID)
	if err != nil {
		return err
	}
	ws.Emit([]string{config.ChUser(conversation.UserID.Hex())}, config.EvSupportTyping,
		TypingSignal{ConversationID: conversationID, Side: string(models.SupportSenderAgent), Name: name})
	return nil
}

// ── desk ────────────────────────────────────────────────────────────────

// ConversationQuery filters the desk list. AgentID is used when Mine is set.
type ConversationQuery struct {
	Status  *models.SupportStatus
	Search  *string
	Mine    bool
	AgentID string
	Page    int64
	Limit   int64
}

// ConversationList is one page of the desk plus the open unread total.
type ConversationList struct {
	Items       []models.ConversationView
	Total       int64
	UnreadTotal int64
}

// ListConversations lists conversations that have at least one message,
// unanswered customers first, then most recent activity.
func ListConversations(ctx context.Context, q ConversationQuery) (ConversationList, error) {
	filter := bson.M{"lastMessageAt": bson.M{"$ne": nil}}
	if q.Status != nil {
		filter["status"] = *q.Status
	}
	if q.Mine && q.AgentID != "" {
		agent, err := bson.ObjectIDFromHex(q.AgentID)
		if err != nil {
			return ConversationList{}, err
		}
		filter["assignedTo"] = agent
	}

	if q.Search != nil {
		if term := jsTrim(*q.Search); term != "" {
			pattern := bson.Regex{Pattern: regexp.QuoteMeta(term), Options: "i"}
			cur, err := mongox.C(models.CollUsers).Find(ctx,
				bson.M{"$or": bson.A{bson.M{"email": pattern}, bson.M{"username": pattern}, bson.M{"fullName": pattern}}},
				options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(200))
			if err != nil {
				return ConversationList{}, err
			}
			var users []struct {
				ID bson.ObjectID `bson:"_id"`
			}
			if err := cur.All(ctx, &users); err != nil {
				return ConversationList{}, err
			}
			ids := bson.A{}
			for _, u := range users {
				ids = append(ids, u.ID)
			}
			filter["userId"] = bson.M{"$in": ids}
		}
	}

	coll := conversations()
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "unreadByAgent", Value: -1}, {Key: "lastMessageAt", Value: -1}}).
		SetSkip((q.Page-1)*q.Limit).
		SetLimit(q.Limit))
	if err != nil {
		return ConversationList{}, err
	}
	var rows []models.SupportConversation
	if err := cur.All(ctx, &rows); err != nil {
		return ConversationList{}, err
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return ConversationList{}, err
	}
	acur, err := coll.Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"status": models.SupportStatusOpen}},
		bson.M{"$group": bson.M{"_id": nil, "total": bson.M{"$sum": "$unreadByAgent"}}},
	})
	if err != nil {
		return ConversationList{}, err
	}
	var unread []struct {
		Total float64 `bson:"total"`
	}
	if err := acur.All(ctx, &unread); err != nil {
		return ConversationList{}, err
	}

	byID := map[bson.ObjectID]*models.SupportUserSummary{}
	if len(rows) > 0 {
		ids := bson.A{}
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		ucur, err := mongox.C(models.CollUsers).Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
			options.Find().SetProjection(bson.M{"email": 1, "username": 1, "fullName": 1}))
		if err != nil {
			return ConversationList{}, err
		}
		var users []userNames
		if err := ucur.All(ctx, &users); err != nil {
			return ConversationList{}, err
		}
		for i := range users {
			byID[users[i].ID] = summaryOf(&users[i])
		}
	}

	items := make([]models.ConversationView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToConversationView(&rows[i], byID[rows[i].UserID]))
	}
	out := ConversationList{Items: items, Total: total}
	if len(unread) > 0 {
		out.UnreadTotal = int64(unread[0].Total)
	}
	return out, nil
}

// GetConversationForDesk is the desk view (with the customer summary).
func GetConversationForDesk(ctx context.Context, conversationID string) (models.ConversationView, error) {
	doc, err := getByID(ctx, conversationID)
	if err != nil {
		return models.ConversationView{}, err
	}
	user, err := userSummary(ctx, doc.UserID)
	if err != nil {
		return models.ConversationView{}, err
	}
	return models.ToConversationView(doc, user), nil
}

// ConversationChange: at least one field is set (the route enforces it).
type ConversationChange struct {
	Status     *models.SupportStatus
	AssignToMe *bool
}

// UpdateResult carries the before/after views for the audit trail.
type UpdateResult struct {
	Before models.ConversationView
	After  models.ConversationView
}

// UpdateConversation resolves, reopens or takes ownership. Each change also
// drops a system line into the thread so the customer sees what happened.
func UpdateConversation(ctx context.Context, conversationID, agentID string, change ConversationChange) (UpdateResult, error) {
	conversation, err := getByID(ctx, conversationID)
	if err != nil {
		return UpdateResult{}, err
	}
	before := models.ToConversationView(conversation, nil)
	name, err := AgentDisplayName(ctx, agentID)
	if err != nil {
		return UpdateResult{}, err
	}

	set := bson.M{}
	var notes []string
	if change.Status != nil && *change.Status != "" && *change.Status != conversation.Status {
		set["status"] = *change.Status
		if *change.Status == models.SupportStatusResolved {
			notes = append(notes, name+" marked this conversation as resolved")
		} else {
			notes = append(notes, name+" reopened this conversation")
		}
	}
	if change.AssignToMe != nil && *change.AssignToMe &&
		(conversation.AssignedTo == nil || conversation.AssignedTo.Hex() != agentID) {
		agent, err := bson.ObjectIDFromHex(agentID)
		if err != nil {
			return UpdateResult{}, err
		}
		set["assignedTo"] = agent
		set["assignedName"] = name
		notes = append(notes, name+" joined the conversation")
	}
	if len(set) == 0 {
		after, err := GetConversationForDesk(ctx, conversationID)
		if err != nil {
			return UpdateResult{}, err
		}
		return UpdateResult{Before: before, After: after}, nil
	}

	updated, err := setAndReturn(ctx, conversation.ID, set)
	if err != nil {
		return UpdateResult{}, err
	}

	for _, note := range notes {
		msg := models.SupportMessage{
			ConversationID: updated.ID,
			Sender:         models.SupportSenderSystem,
			SenderName:     "System",
			Body:           note,
			CreatedAt:      mongox.Now(),
		}
		res, err := messages().InsertOne(ctx, msg)
		if err != nil {
			return UpdateResult{}, err
		}
		msg.ID, _ = res.InsertedID.(bson.ObjectID)
		ws.Emit([]string{config.ChUser(updated.UserID.Hex()), config.ChAdminSupport},
			config.EvSupportMessage, models.ToMessageView(&msg))
	}

	after, err := broadcastConversation(ctx, updated)
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Before: before, After: after}, nil
}
