// Package backend is the private boundary between SDK behavior and a core version.
package backend

import (
	"context"
	"github.com/HyperPteLtd/libbox/internal/contract"
)

type Factory interface {
	Check(context.Context, string, contract.Options) error
	Build(context.Context, string, contract.Options, contract.Hooks, contract.Network, contract.Emit) (Runtime, error)
}

type Runtime interface {
	Start() error
	Close() error
	Status() contract.RuntimeStatus
	Outbounds() []contract.Outbound
	Groups() []contract.Group
	SelectOutbound(string, string) error
	URLTest(context.Context, string, string) (int32, error)
	CloseConnections() error
	SetPaused(bool) error
	UpdateNetwork(contract.Network) error
}
