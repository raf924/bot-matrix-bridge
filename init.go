package main

import (
	"github.com/raf924/bot-matrix-bridge/pkg"
	"github.com/raf924/connector-sdk/rpc"
)

func init() {
	rpc.RegisterConnectorRelay("matrix", pkg.NewMatrixConnector)
}
