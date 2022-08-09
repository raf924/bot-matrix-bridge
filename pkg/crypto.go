package pkg

import (
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type stateStore struct {
	room     id.RoomID
	encEvent *event.EncryptionEventContent
}

func NewStateStore(room id.RoomID, encEvent *event.EncryptionEventContent) *stateStore {
	return &stateStore{room: room, encEvent: encEvent}
}

func (s *stateStore) IsEncrypted(id id.RoomID) bool {
	return id == s.room && s.encEvent != nil && s.encEvent.Algorithm != ""
}

func (s *stateStore) GetEncryptionEvent(id id.RoomID) *event.EncryptionEventContent {
	if id == s.room {
		return s.encEvent
	}
	return nil
}

func (s *stateStore) FindSharedRooms(_ id.UserID) []id.RoomID {
	return []id.RoomID{}
}
