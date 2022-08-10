package pkg

import (
	"maunium.net/go/mautrix/crypto"
	"sync"
)

type matrixUser struct {
	mutex   *sync.Mutex
	machine *crypto.OlmMachine
}
