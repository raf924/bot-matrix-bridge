package pkg

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/raf924/bot-matrix-bridge/pkg/utils"
	"github.com/raf924/connector-sdk/domain"
	"github.com/raf924/connector-sdk/rpc"
	"github.com/raf924/queue"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/appservice/sqlstatestore"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
	"regexp"
	"strings"
)

var mentionRegex = regexp.MustCompile(`(?m)<a href="https://matrix\.to/#/@connector_(\w+):[^"]+">\w+</a>:?`)

func NewMatrixConnector(config interface{}) rpc.ConnectorRelay {
	b := new(bytes.Buffer)
	err := yaml.NewEncoder(b).Encode(config)
	if err != nil {
		panic(err)
	}
	{
		config := MatrixConfig{
			AppService: appservice.Create(),
		}
		err = yaml.NewDecoder(b).Decode(&config)
		if err != nil {
			panic(err)
		}
		return &matrixBridge{
			config: config,
		}
	}
}

type matrixBridge struct {
	*utils.RunningContext
	botUser         *domain.User
	config          MatrixConfig
	messageConsumer queue.Consumer[*domain.ClientMessage]
	messageQueue    queue.Queue[*domain.ClientMessage]
	dispatcherGiven bool
	users           domain.UserList
	appService      *appservice.AppService
}

func (m *matrixBridge) matrixMention(userID id.UserID) string {
	displayName := userID.String()
	members, err := m.appService.BotIntent().JoinedMembers(id.RoomID(m.config.Room))
	if err == nil {
		displayName = members.Joined[userID].DisplayName
	}
	return fmt.Sprintf("%s: ", displayName)
}

func (m *matrixBridge) formattedMatrixMention(userID id.UserID) string {
	displayName := userID.String()
	members, err := m.appService.BotIntent().JoinedMembers(id.RoomID(m.config.Room))
	if err == nil {
		displayName = members.Joined[userID].DisplayName
	}
	return fmt.Sprintf("<a href='%s'>%s</a>", userID.URI().MatrixToURL(), displayName)
}

func (m *matrixBridge) Dispatch(serverMessage domain.ServerMessage) error {
	var err error
	switch message := serverMessage.(type) {
	case *domain.ChatMessage:
		messageBody := message.Message()
		formattedMessageBody := messageBody
		msgType := event.MsgText
		if message.MentionsConnectorUser() {
			messageBody = strings.ReplaceAll(messageBody, "@"+m.botUser.Nick(), m.matrixMention(id.UserID(m.config.User)))
			formattedMessageBody = strings.ReplaceAll(formattedMessageBody, "@"+m.botUser.Nick(), m.formattedMatrixMention(id.UserID(m.config.User)))
		}
		for _, user := range message.Recipients() {
			messageBody = strings.ReplaceAll(messageBody, "@"+user.Nick(), m.matrixMention(m.ghostId(user)))
			formattedMessageBody = strings.ReplaceAll(formattedMessageBody, "@"+user.Nick(), m.formattedMatrixMention(m.ghostId(user)))
		}
		client := func() *mautrix.Client {
			if message.Private() {
				if !message.Sender().Is(m.botUser) {
					messageBody = m.matrixMention(id.UserID(m.config.User)) + "\n Private message from" + message.Sender().Nick() + "#" + message.Sender().Id() + ":\n" + messageBody
					formattedMessageBody = m.formattedMatrixMention(id.UserID(m.config.User)) + "<br>Private message from: " + message.Sender().Nick() + "#" + message.Sender().Id() + ":<br>" + formattedMessageBody
				} else {
					msgType = event.MsgNotice
				}
				return m.appService.BotClient()
			} else if message.Sender().Is(m.botUser) {
				return nil
			}
			return m.appService.Client(m.ghostId(message.Sender()))
		}()
		if client == nil {
			return nil
		}
		content := event.MessageEventContent{
			MsgType:       msgType,
			Body:          messageBody,
			FormattedBody: formattedMessageBody,
			Format:        event.FormatHTML,
		}
		_, err = client.SendMessageEvent(id.RoomID(m.config.Room), event.EventMessage, &content)
	case *domain.UserEvent:
		switch message.EventType() {
		case domain.UserJoined:
			m.users.Add(message.User())
			err = m.createMatrixUser(message.User(), true)
			if err != nil {
				return err
			}
			_, err = m.appService.BotIntent().SendNotice(id.RoomID(m.config.Room), message.User().Nick()+" has joined")
		case domain.UserLeft:
			_, err = m.appService.BotIntent().SendNotice(id.RoomID(m.config.Room), message.User().Nick()+" has left")
			if err != nil {
				return err
			}
			user := m.users.Find(message.User().Nick())
			if user != nil {
				_, _ = m.appService.Intent(m.ghostId(user)).LeaveRoom(id.RoomID(m.config.Room))
			}
			m.users.Remove(message.User())
		}
	}
	return err
}

func (m *matrixBridge) Commands() domain.CommandList {
	return domain.NewCommandList()
}

func (m *matrixBridge) initAppService() error {
	m.appService = m.config.AppService
	m.appService.LogConfig.PrintLevel = -10
	_, err := m.appService.Init()
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite3", m.config.SqliteDb)
	if err != nil {
		return err
	}
	withDB, err := dbutil.NewWithDB(db, "sqlite3")
	if err != nil {
		return err
	}
	stateStore := sqlstatestore.NewSQLStateStore(withDB, dbutil.MauLogger(m.appService.Log.Sub("statestore")))
	err = stateStore.Upgrade()
	if err != nil {
		return err
	}
	m.appService.StateStore = stateStore
	return nil
}

func (m *matrixBridge) startAppService() error {
	err := m.appService.BotIntent().SetDisplayName(m.config.DisplayName)
	if err != nil {
		return err
	}
	err = m.appService.BotIntent().EnsureJoined(id.RoomID(m.config.Room))
	if err != nil {
		return err
	}
	err = m.appService.BotIntent().StateEvent(id.RoomID(m.config.Room), event.StateEncryption, "", &event.EncryptionEventContent{})
	if err == nil {
		return fmt.Errorf("room `%s` is encrypted", m.config.Room)
	}
	ep := appservice.NewEventProcessor(m.appService)
	ep.On(event.EventMessage, func(evt *event.Event) {
		println(evt.Type.Type, evt.Sender, evt.RoomID, evt.Content.AsMessage().FormattedBody)
		m.handleMessage(evt)
	})
	m.appService.Ready = true
	go func() {
		_ = m.Critical(func(ctx context.Context) error {
			ep.Start()
			return fmt.Errorf("event processor stopped")
		})
	}()
	go func() {
		_ = m.Critical(func(ctx context.Context) error {
			m.appService.Start()
			return fmt.Errorf("appservice stopped")
		})
	}()
	m.OnDone(func() {
		m.appService.Stop()
		ep.Stop()
	})
	return nil
}

func (m *matrixBridge) Start(ctx context.Context, botUser *domain.User, onlineUsers domain.UserList, _ string) error {
	var err error
	m.RunningContext = utils.Runnable(ctx, func(ctx context.Context) error {
		m.messageQueue = queue.NewQueue[*domain.ClientMessage]()
		m.messageConsumer, err = m.messageQueue.NewConsumer()
		if err != nil {
			return err
		}
		m.dispatcherGiven = false
		m.botUser = botUser
		m.users = domain.NewUserList(onlineUsers.All()...)
		err = m.initAppService()
		if err != nil {
			return fmt.Errorf("failed to initialize appservice: %w", err)
		}
		err = m.startAppService()
		if err != nil {
			return err
		}
		members, err := m.appService.BotIntent().JoinedMembers(id.RoomID(m.config.Room))
		if err != nil {
			return err
		}
		for _, user := range m.users.All() {
			if user.Is(botUser) {
				continue
			}
			ghostId := m.ghostId(user)
			if _, ok := members.Joined[ghostId]; ok {
				delete(members.Joined, ghostId)
			}
			err := m.createMatrixUser(user, false)
			if err != nil {
				return err
			}
		}
		for userID := range members.Joined {
			if userID == m.appService.BotMXID() || userID.Localpart() == m.config.User {
				continue
			}
			_, _ = m.appService.Intent(userID).LeaveRoom(id.RoomID(m.config.Room))
		}
		return nil
	})
	return m.Run()
}

func (m *matrixBridge) ghostId(user *domain.User) id.UserID {
	return id.NewUserID(fmt.Sprintf("connector_%s", user.Nick()), m.appService.HomeserverDomain)
}

func (m *matrixBridge) handleMessage(evt *event.Event) {
	if evt.Sender.String() != m.config.User {
		return
	}
	content := evt.Content.AsMessage()
	if content.MsgType != event.MsgText && content.MsgType != event.MsgEmote {
		return
	}
	var body string
	if content.FormattedBody == "" {
		body = content.Body
	} else {
		body = mentionRegex.ReplaceAllStringFunc(content.FormattedBody, func(s string) string {
			return "@" + mentionRegex.FindStringSubmatch(s)[1]
		})
	}
	err := m.Critical(func(ctx context.Context) error {
		switch content.MsgType {
		case event.MsgText:
			return m.messageQueue.Produce(domain.NewClientMessage(body, nil, false))
		case event.MsgEmote:
			return m.messageQueue.Produce(domain.NewEmote(body))
		default:
			return errors.New("invalid message type")
		}
	})
	if err != nil {
		println("error while handling message:", err.Error())
	}
	err = m.appService.BotIntent().MarkRead(evt.RoomID, evt.ID)
	if err != nil {
		return
	}
}

func (m *matrixBridge) createMatrixUser(user *domain.User, new bool) error {
	var err error
	userID := m.ghostId(user)
	err = m.appService.Intent(userID).EnsureRegistered()
	if err != nil {
		return err
	}
	err = m.appService.BotIntent().EnsureInvited(id.RoomID(m.config.Room), userID)
	if err != nil {
		return err
	}
	err = m.appService.Intent(userID).EnsureJoined(id.RoomID(m.config.Room))
	if err != nil {
		return err
	}
	if new {
		_, err = m.appService.BotIntent().SendNotice(id.RoomID(m.config.Room), user.Nick()+" has joined")
		if err != nil {
			return err
		}
	}
	err = m.appService.Intent(userID).SetDisplayName(user.Nick())
	if err != nil {
		return err
	}
	return nil
}

func (m *matrixBridge) Accept() (rpc.Dispatcher, error) {
	if m.dispatcherGiven {
		ctx, can := context.WithCancel(m)
		<-ctx.Done()
		can()
		return nil, ctx.Err()
	}
	m.dispatcherGiven = true
	return m, nil
}

func (m *matrixBridge) Recv() (*domain.ClientMessage, error) {
	return m.messageConsumer.Consume(m)
}
