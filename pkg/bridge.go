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
	"log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/appservice/sqlstatestore"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"
	"net/url"
	"regexp"
	"strings"
)

const upperCasePrefix = "="

var mentionRegex = regexp.MustCompile(`(?m)<a href="https://matrix\.to/#/@connector_([0-9a-z-.=_/]+):[^"]+">\w+</a>:?`)
var replyRegex = regexp.MustCompile(`(?i)<mx-reply>.*</mx-reply>(.+)`)

func allowedImageLinksRegex(allowedDomains []string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`(?mi)https://([a-zA-Z0-9]+\.)?(%s)(/.+)+\.(jpg|jpeg|png)`, strings.ReplaceAll(strings.Join(allowedDomains, "|"), ".", "\\.")))
}

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
			config:            config,
			allowedImageRegex: allowedImageLinksRegex(config.ImageDisplay.AllowedDomains),
		}
	}
}

var _ rpc.ConnectorRelay = (*matrixBridge)(nil)
var _ appservice.QueryHandler = (*matrixBridge)(nil)

type matrixBridge struct {
	*utils.RunningContext
	botUser           *domain.User
	config            MatrixConfig
	messageConsumer   queue.Consumer[*domain.ClientMessage]
	messageQueue      queue.Queue[*domain.ClientMessage]
	dispatcherGiven   bool
	users             domain.UserList
	matrixUsers       map[id.UserID]string
	appService        *appservice.AppService
	allowedImageRegex *regexp.Regexp
}

func (m *matrixBridge) QueryAlias(alias string) bool {
	resp, err := m.appService.BotClient().ResolveAlias(id.RoomAlias(alias))
	if err != nil {
		println(err)
		return false
	}
	return resp.RoomID.String() == m.config.Room
}

func (m *matrixBridge) QueryUser(userID id.UserID) bool {
	_, ok := m.matrixUsers[userID]
	return ok
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
		senderGhostId := m.ghostId(message.Sender())
		senderIntent := m.appService.Intent(senderGhostId)
		if m.config.ImageDisplay.Enabled {
			submatches := m.allowedImageRegex.FindAllStringSubmatch(message.Message(), -1)
			for _, submatch := range submatches {
				imgUrl, err := url.Parse(submatch[0])
				if err != nil {
					println("could not parse image link", submatch[0])
					continue
				}
				go func(imgUrl string) {
					resp, err := senderIntent.UploadLink(imgUrl)
					if err != nil {
						println("could not get image", imgUrl, err.Error())
						return
					}
					_, err = senderIntent.SendMessageEvent(id.RoomID(m.config.Room), event.EventMessage, &event.MessageEventContent{
						MsgType: event.MsgImage,
						URL:     resp.ContentURI.CUString(),
					})
					if err != nil {
						println("could not send image", imgUrl, err.Error())
						return
					}
				}(imgUrl.String())
			}
		}
		messageEvent := format.RenderMarkdown(message.Message(), true, false)
		if messageEvent.FormattedBody == "" {
			messageEvent.FormattedBody = messageEvent.Body
			messageEvent.Format = event.FormatHTML
		}
		if message.MentionsConnectorUser() {
			messageEvent.Body = strings.ReplaceAll(messageEvent.Body, "@"+m.botUser.Nick(), m.matrixMention(id.UserID(m.config.User)))
			messageEvent.FormattedBody = strings.ReplaceAll(messageEvent.FormattedBody, "@"+m.botUser.Nick(), m.formattedMatrixMention(id.UserID(m.config.User)))
		}
		for _, user := range message.Recipients() {
			messageEvent.Body = strings.ReplaceAll(messageEvent.Body, "@"+user.Nick(), m.matrixMention(m.ghostId(user)))
			messageEvent.FormattedBody = strings.ReplaceAll(messageEvent.FormattedBody, "@"+user.Nick(), m.formattedMatrixMention(m.ghostId(user)))
		}
		client := func() *mautrix.Client {
			if message.Private() {
				if !message.Sender().Is(m.botUser) {
					messageEvent.Body = m.matrixMention(id.UserID(m.config.User)) + "\n Private message from" + message.Sender().Nick() + "#" + message.Sender().Id() + ":\n" + messageEvent.Body
					messageEvent.FormattedBody = m.formattedMatrixMention(id.UserID(m.config.User)) + "<br>Private message from: " + message.Sender().Nick() + "#" + message.Sender().Id() + ":<br>" + messageEvent.FormattedBody
				} else {
					messageEvent.MsgType = event.MsgNotice
				}
				return m.appService.BotClient()
			} else if message.Sender().Is(m.botUser) {
				return nil
			}
			return m.appService.Client(senderGhostId)
		}()
		if client == nil {
			return nil
		}
		_, err = client.SendMessageEvent(id.RoomID(m.config.Room), event.EventMessage, messageEvent)
	case *domain.UserEvent:
		switch message.EventType() {
		case domain.UserJoined:
			m.users.Add(message.User())
			err = m.createMatrixUser(message.User())
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
				ghostId := m.ghostId(user)
				_, _ = m.appService.Intent(ghostId).LeaveRoom(id.RoomID(m.config.Room))
				delete(m.matrixUsers, ghostId)
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
	m.appService.QueryHandler = m
	m.appService.Ready = true
	ep.Start()
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
		m.matrixUsers = map[id.UserID]string{}
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
			go func(user *domain.User) {
				_ = m.Critical(func(ctx context.Context) error {
					return m.createMatrixUser(user)
				})
			}(user)
		}
		for userID := range members.Joined {
			if userID == m.appService.BotMXID() || userID.String() == m.config.User || userID.Localpart() == m.config.User {
				continue
			}
			go func(userIntent *appservice.IntentAPI) {
				_, err := userIntent.LeaveRoom(id.RoomID(m.config.Room))
				log.Printf("failed to force absent user to leave: %v\n", err)
			}(m.appService.Intent(userID))
		}
		return nil
	})
	return m.Run()
}

func (m *matrixBridge) ghostId(user *domain.User) id.UserID {
	validMatrixLocalPart := regexp.MustCompile("(?)([A-Z])").ReplaceAllStringFunc(user.Nick(), func(s string) string {
		return upperCasePrefix + strings.ToLower(s)
	})
	return id.NewUserID(fmt.Sprintf("connector_%s", validMatrixLocalPart), m.appService.HomeserverDomain)
}

func (m *matrixBridge) formatMessageBody(content *event.MessageEventContent, followReply bool) string {
	var body string
	if content.FormattedBody == "" {
		body = content.Body
	} else {
		if followReply && content.RelatesTo.GetReplyTo() != "" {
			submatches := replyRegex.FindAllStringSubmatch(content.FormattedBody, -1)
			content.FormattedBody = submatches[0][1]
			repliedEvent, err := m.appService.BotClient().GetEvent(id.RoomID(m.config.Room), content.RelatesTo.GetReplyTo())
			if err == nil {
				body = ">" + m.formatMessageBody(repliedEvent.Content.AsMessage(), false) + "\n\n"
			}
		}
		body += mentionRegex.ReplaceAllStringFunc(content.FormattedBody, func(s string) string {
			return "@" + regexp.MustCompile("(?)"+upperCasePrefix+"[a-z]").ReplaceAllStringFunc(mentionRegex.FindStringSubmatch(s)[1], func(s string) string {
				return strings.ToUpper(strings.TrimPrefix(s, upperCasePrefix))
			})
		})
	}
	return body
}

func (m *matrixBridge) handleMessage(evt *event.Event) {
	if evt.Sender.String() != m.config.User {
		return
	}
	if evt.RoomID.String() != m.config.Room {
		return
	}
	content := evt.Content.AsMessage()
	if content.MsgType != event.MsgText && content.MsgType != event.MsgEmote {
		return
	}
	body := m.formatMessageBody(content, true)
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
		return
	}
	err = m.appService.BotIntent().MarkRead(evt.RoomID, evt.ID)
	if err != nil {
		return
	}
}

func (m *matrixBridge) createMatrixUser(user *domain.User) error {
	var err error
	userID := m.ghostId(user)
	err = m.appService.Intent(userID).EnsureRegistered()
	if err != nil {
		return err
	}
	err = m.appService.Intent(userID).SetDisplayName(user.Nick())
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
	m.matrixUsers[userID] = user.Nick()
	return nil
}

func (m *matrixBridge) Accept() (rpc.Dispatcher, error) {
	if m.dispatcherGiven {
		ctx, can := context.WithCancel(m)
		<-ctx.Done()
		can()
		return nil, fmt.Errorf("dispatcher was cancelled: %w", ctx.Err())
	}
	m.dispatcherGiven = true
	return m, nil
}

func (m *matrixBridge) Recv() (*domain.ClientMessage, error) {
	return m.messageConsumer.Consume(m)
}
