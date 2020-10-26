package node
import (
	"path/filepath"
	"reflect"
	"github.com/Cryptochain-VON/accounts"
	"github.com/Cryptochain-VON/core/rawdb"
	"github.com/Cryptochain-VON/ethdb"
	"github.com/Cryptochain-VON/event"
	"github.com/Cryptochain-VON/p2p"
	"github.com/Cryptochain-VON/rpc"
)
type ServiceContext struct {
	services       map[reflect.Type]Service 
	Config         Config
	EventMux       *event.TypeMux    
	AccountManager *accounts.Manager 
}
func (ctx *ServiceContext) OpenDatabase(name string, cache int, handles int, namespace string) (ethdb.Database, error) {
	if ctx.Config.DataDir == "" {
		return rawdb.NewMemoryDatabase(), nil
	}
	return rawdb.NewLevelDBDatabase(ctx.Config.ResolvePath(name), cache, handles, namespace)
}
func (ctx *ServiceContext) OpenDatabaseWithFreezer(name string, cache int, handles int, freezer string, namespace string) (ethdb.Database, error) {
	if ctx.Config.DataDir == "" {
		return rawdb.NewMemoryDatabase(), nil
	}
	root := ctx.Config.ResolvePath(name)
	switch {
	case freezer == "":
		freezer = filepath.Join(root, "ancient")
	case !filepath.IsAbs(freezer):
		freezer = ctx.Config.ResolvePath(freezer)
	}
	return rawdb.NewLevelDBDatabaseWithFreezer(root, cache, handles, freezer, namespace)
}
func (ctx *ServiceContext) ResolvePath(path string) string {
	return ctx.Config.ResolvePath(path)
}
func (ctx *ServiceContext) Service(service interface{}) error {
	element := reflect.ValueOf(service).Elem()
	if running, ok := ctx.services[element.Type()]; ok {
		element.Set(reflect.ValueOf(running))
		return nil
	}
	return ErrServiceUnknown
}
func (ctx *ServiceContext) ExtRPCEnabled() bool {
	return ctx.Config.ExtRPCEnabled()
}
type ServiceConstructor func(ctx *ServiceContext) (Service, error)
type Service interface {
	Protocols() []p2p.Protocol
	APIs() []rpc.API
	Start(server *p2p.Server) error
	Stop() error
}
