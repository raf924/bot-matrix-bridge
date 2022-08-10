package pkg

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"github.com/raf924/bot-matrix-bridge/pkg/utils"
	"github.com/raf924/connector-sdk/domain"
	"github.com/raf924/connector-sdk/rpc"
	"github.com/raf924/queue"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

func NewMatrixConnector(config interface{}) rpc.ConnectorRelay {
	b := new(bytes.Buffer)
	err := yaml.NewEncoder(b).Encode(config)
	if err != nil {
		panic(err)
	}
	{
		config := MatrixConfig{}
		err = yaml.NewDecoder(b).Decode(&config)
		if err != nil {
			panic(err)
		}
		return &matrixBridge{
			config:      config,
			client:      nil,
			userClients: map[string]*crypto.OlmMachine{},
		}
	}
}

type matrixBridge struct {
	*utils.RunningContext
	client          *mautrix.Client
	olmMachine      *crypto.OlmMachine
	botUser         *domain.User
	config          MatrixConfig
	userClients     map[string]*crypto.OlmMachine
	stateStore      *stateStore
	crytpoStore     *crypto.SQLCryptoStore
	db              *sql.DB
	messageConsumer queue.Consumer[*domain.ClientMessage]
	messageQueue    queue.Queue[*domain.ClientMessage]
	dispatcherGiven bool
	users           domain.UserList
	guestMutex      sync.Mutex
}

func (m *matrixBridge) matrixMention(userID id.UserID) string {
	displayName := userID.String()
	members, err := m.client.JoinedMembers(id.RoomID(m.config.Room))
	if err == nil && members.Joined[userID].DisplayName != nil {
		displayName = *members.Joined[userID].DisplayName
	}
	return fmt.Sprintf("%s: ", displayName)
}

func (m *matrixBridge) formattedMatrixMention(userID id.UserID) string {
	displayName := userID.String()
	members, err := m.client.JoinedMembers(id.RoomID(m.config.Room))
	if err == nil && members.Joined[userID].DisplayName != nil {
		displayName = *members.Joined[userID].DisplayName
	}
	return fmt.Sprintf("<a href='%s'>%s</a>", userID.URI().MatrixToURL(), displayName)
}

func (m *matrixBridge) Dispatch(serverMessage domain.ServerMessage) error {
	var err error
	switch message := serverMessage.(type) {
	case *domain.ChatMessage:
		if message.Sender().Is(m.botUser) {
			return nil
		}
		messageBody := message.Message()
		formattedMessageBody := messageBody
		if message.MentionsConnectorUser() {
			messageBody = strings.ReplaceAll(messageBody, "@"+m.botUser.Nick(), m.matrixMention(id.UserID(m.config.User)))
			formattedMessageBody = strings.ReplaceAll(formattedMessageBody, "@"+m.botUser.Nick(), m.formattedMatrixMention(id.UserID(m.config.User)))
		}
		for _, user := range message.Recipients() {
			messageBody = strings.ReplaceAll(messageBody, "@"+user.Nick(), m.matrixMention(m.userClients[user.Nick()+"#"+user.Id()].Client.UserID))
			formattedMessageBody = strings.ReplaceAll(formattedMessageBody, "@"+user.Nick(), m.formattedMatrixMention(m.userClients[user.Nick()+"#"+user.Id()].Client.UserID))
		}

		olmMachine := func() *crypto.OlmMachine {
			if message.Private() {
				messageBody = m.matrixMention(id.UserID(m.config.User)) + "\n Private message from" + message.Sender().Nick() + "#" + message.Sender().Id() + ":\n" + messageBody
				formattedMessageBody = m.formattedMatrixMention(id.UserID(m.config.User)) + "<br>Private message from: " + message.Sender().Nick() + "#" + message.Sender().Id() + ":<br>" + formattedMessageBody
				return m.olmMachine
			}
			return m.userClients[message.Sender().Nick()+"#"+message.Sender().Id()]
		}()
		content := event.MessageEventContent{
			MsgType:       event.MsgText,
			Body:          messageBody,
			FormattedBody: formattedMessageBody,
			Format:        event.FormatHTML,
		}
		var evt *event.EncryptedEventContent
		evt, err = olmMachine.EncryptMegolmEvent(id.RoomID(m.config.Room), event.EventMessage, &content)
		if err != nil {
			println(err.Error())
			_, err = olmMachine.Client.SendMessageEvent(id.RoomID(m.config.Room), event.EventMessage, &content)
		} else {
			_, err = olmMachine.Client.SendMessageEvent(id.RoomID(m.config.Room), event.EventEncrypted, evt)
		}
	case *domain.UserEvent:
		switch message.EventType() {
		case domain.UserJoined:
			err = m.createMatrixUser(message.User())
			if err != nil {
				return err
			}
			_, err = m.client.SendNotice(id.RoomID(m.config.Room), message.User().Nick()+" has joined")
		case domain.UserLeft:
			_, err = m.client.SendNotice(id.RoomID(m.config.Room), message.User().Nick()+" has left")
			if err != nil {
				return err
			}
			user := m.users.Find(message.User().Nick())
			if user != nil {
				if c, ok := m.userClients[user.Nick()+"#"+user.Id()]; ok {
					_, err = c.Client.LeaveRoom(id.RoomID(m.config.Room))
				}
			}
		}
	}
	return err
}

func (m *matrixBridge) Commands() domain.CommandList {
	return domain.NewCommandList()
}

func findDeviceId(db *sql.DB, userID id.UserID) (id.DeviceID, bool) {
	query, err := db.Query("SELECT device_id FROM crypto_account WHERE account_id = ?", userID)
	if err != nil {
		return "", false
	}
	if !query.Next() {
		return "", false
	}
	deviceId := ""
	err = query.Scan(&deviceId)
	if err != nil {
		return "", false
	}
	return id.DeviceID(deviceId), true
}

func (m *matrixBridge) login() error {
	deviceId, found := findDeviceId(m.db, m.client.UserID)
	if !found {
		deviceId = ""
	}
	_, err := m.client.Login(&mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: m.config.Bot,
		},
		DeviceID:                 deviceId,
		Password:                 m.config.Password,
		InitialDeviceDisplayName: "Hack.chat bot",
		StoreCredentials:         true,
	})
	return err
}

func (m *matrixBridge) Start(ctx context.Context, botUser *domain.User, onlineUsers domain.UserList, _ string) error {
	var err error
	m.RunningContext, err = utils.Run(ctx, func(ctx context.Context) error {
		m.guestMutex = sync.Mutex{}
		m.dispatcherGiven = false
		m.botUser = botUser
		m.users = onlineUsers
		start := time.Now()
		m.client, err = mautrix.NewClient(m.config.Url, id.UserID(m.config.Bot), m.config.AccessToken)
		if err != nil {
			return err
		}
		m.db, err = sql.Open("sqlite3", m.config.SqliteDb)
		if err != nil {
			return err
		}
		err = crypto.NewSQLCryptoStore(m.db, "sqlite3", m.client.UserID.String(), m.client.DeviceID, []byte("test"), &logger{}).CreateTables()
		if err != nil {
			return err
		}
		err = m.login()
		if err != nil {
			return err
		}
		m.crytpoStore = crypto.NewSQLCryptoStore(m.db, "sqlite3", m.client.UserID.String(), m.client.DeviceID, []byte("test"), &logger{})
		m.stateStore = NewStateStore(id.RoomID(m.config.Room), &event.EncryptionEventContent{})
		m.client.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.StateEncryption, func(source mautrix.EventSource, evt *event.Event) {
			m.stateStore.encEvent = evt.Content.AsEncryption()
		})
		m.olmMachine, err = m.createOlmMachine(m.client)
		if err != nil {
			return err
		}
		m.userClients[m.botUser.Nick()+"#"+m.botUser.Id()] = m.olmMachine
		members, err := m.client.Members(id.RoomID(m.config.Room), mautrix.ReqMembers{NotMembership: "leave"}, mautrix.ReqMembers{NotMembership: "ban"})
		if err != nil {
			return err
		}
		for _, evt := range members.Chunk {
			memberEventContent := evt.Content.AsMember()
			println(evt.Sender, evt.GetStateKey(), memberEventContent.Displayname, memberEventContent.Membership)
			if evt.GetStateKey() == m.client.UserID.String() || evt.GetStateKey() == m.config.User {
				continue
			}
			time.Sleep(500 * time.Millisecond)
			_, err := m.client.KickUser(id.RoomID(m.config.Room), &mautrix.ReqKickUser{
				Reason: "",
				UserID: id.UserID(evt.GetStateKey()),
			})
			if err != nil {
				println(err.Error())
			}
		}
		for _, user := range onlineUsers.All() {
			if user.Is(botUser) {
				continue
			}
			err = m.createMatrixUser(user)
			if err != nil {
				return err
			}
		}
		m.messageQueue = queue.NewQueue[*domain.ClientMessage]()
		m.messageConsumer, err = m.messageQueue.NewConsumer()
		if err != nil {
			return err
		}
		_, err = m.olmMachine.GenerateAndUploadCrossSigningKeys(m.config.Password, m.config.Passphrase)
		if err != nil {
			return err
		}
		m.client.Syncer.(*mautrix.DefaultSyncer).OnEvent(func(source mautrix.EventSource, evt *event.Event) {
			println("event", source.String(), evt.Type.Type, "-", evt.Type.Class.Name(), evt.GetStateKey())
		})
		m.client.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.ToDeviceEncrypted, func(source mautrix.EventSource, evt *event.Event) {
			println("encrypted to device", source.String(), evt.Type.Type, evt.Type.Class.Name(), evt.Sender, evt.GetStateKey())
		})
		m.client.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.EventEncrypted, func(source mautrix.EventSource, evt *event.Event) {
			encrypted := evt.Content.AsEncrypted()
			println("encrypted", source.String(), evt.Type.Type, evt.Type.Class.Name(), evt.Sender, evt.GetStateKey(), encrypted.Algorithm)
			if time.UnixMilli(evt.Timestamp).Before(start) {
				return
			}
			if evt.Sender == m.client.UserID {
				return
			}
			go func(evt *event.Event, encrypted *event.EncryptedEventContent) {
				megolmEvent, err := m.olmMachine.DecryptMegolmEvent(evt)
				if err != nil {
					println(err.Error())
					return
				}
				println("decrypted", megolmEvent.Type.Type, megolmEvent.Type.Class.Name(), megolmEvent.Content.AsMessage().Body, megolmEvent.Content.AsMessage().FormattedBody)
				m.handleMessage(megolmEvent)
			}(evt, encrypted)
		})
		m.client.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.EventMessage, func(source mautrix.EventSource, evt *event.Event) {
			println("message", evt.Content.AsMessage().Body, evt.Content.AsMessage().FormattedBody)
			if evt.Sender != m.client.UserID {
				return
			}
			m.handleMessage(evt)
		})
		go func() {
			err = m.Critical(func(ctx context.Context) error {
				return m.client.SyncWithContext(ctx)
			})
			if err != nil {
				println(err.Error())
			}
		}()
		return nil
	})
	return err
}

func (m *matrixBridge) handleMessage(evt *event.Event) {
	if evt.Sender.String() != m.config.User {
		return
	}
	content := evt.Content.AsMessage()
	for s := range m.userClients {
		if strings.Contains(content.Body, s+":") {
			content.Body = strings.ReplaceAll(content.Body, s+":", "@"+s)
		}
	}
	_ = m.Critical(func(ctx context.Context) error {
		return m.messageQueue.Produce(domain.NewClientMessage(content.Body, nil, false))
	})
}

func (m *matrixBridge) createOlmMachine(client *mautrix.Client) (*crypto.OlmMachine, error) {
	machine := crypto.NewOlmMachine(client, &logger{}, m.crytpoStore, m.stateStore)
	err := machine.Load()
	if err != nil {
		return nil, err
	}
	client.Syncer.(*mautrix.DefaultSyncer).OnSync(func(resp *mautrix.RespSync, since string) bool {
		machine.ProcessSyncResponse(resp, since)
		return true
	})
	client.Syncer.(*mautrix.DefaultSyncer).OnEventType(event.StateMember, func(source mautrix.EventSource, evt *event.Event) {
		machine.HandleMemberEvent(evt)
	})
	return machine, nil
}

func (m *matrixBridge) createMatrixUser(user *domain.User) error {
	m.guestMutex.Lock()
	time.Sleep(1 * time.Second)
	err := func() error {
		userHash := user.Nick() + "#" + user.Id()
		err := fmt.Errorf("")
		backoff := 0
		var guest *mautrix.RespRegister
		for err != nil {
			time.Sleep(time.Duration(backoff) * time.Second)
			guest, _, err = m.client.RegisterGuest(&mautrix.ReqRegister{
				InitialDeviceDisplayName: userHash,
			})
			backoff = backoff*2 + 1
		}

		if err != nil {
			return err
		}
		client, err := mautrix.NewClient(m.config.Url, guest.UserID, guest.AccessToken)
		if err != nil {
			return err
		}
		_, err = client.JoinRoomByID(id.RoomID(m.config.Room))
		if err != nil {
			compile, _ := regexp.Compile("https://matrix-client\\.matrix\\.org/_matrix/consent\\?u=(.*)&h=([^.]*)")
			consentUrl, _ := url.Parse(compile.FindString(err.Error()))
			formValues := consentUrl.Query()
			formValues.Set("v", "1.0")
			_, err = http.PostForm("https://matrix-client.matrix.org/_matrix/consent", formValues)
			if err != nil {
				return err
			}
		}
		_, err = m.client.InviteUser(id.RoomID(m.config.Room), &mautrix.ReqInviteUser{
			Reason: "test",
			UserID: client.UserID,
		})
		if err != nil {
			return err
		}
		_, err = client.JoinRoomByID(id.RoomID(m.config.Room))
		if err != nil {
			return err
		}
		err = client.SetDisplayName(user.Nick() + "#" + user.Id())
		if err != nil {
			return err
		}
		m.userClients[userHash], err = m.createOlmMachine(client)
		return err
	}()
	m.guestMutex.Unlock()
	return err
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
