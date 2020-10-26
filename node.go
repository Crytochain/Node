package node
import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"github.com/Cryptochain-VON/accounts"
	"github.com/Cryptochain-VON/core/rawdb"
	"github.com/Cryptochain-VON/ethdb"
	"github.com/Cryptochain-VON/event"
	"github.com/Cryptochain-VON/internal/debug"
	"github.com/Cryptochain-VON/log"
	"github.com/Cryptochain-VON/p2p"
	"github.com/Cryptochain-VON/rpc"
	"github.com/prometheus/tsdb/fileutil"
)
type Node struct {
	eventmux *event.TypeMux 
	config   *Config
	accman   *accounts.Manager
	ephemeralKeystore string            
	instanceDirLock   fileutil.Releaser 
	serverConfig p2p.Config
	server       *p2p.Server 
	serviceFuncs []ServiceConstructor     
	services     map[reflect.Type]Service 
	rpcAPIs       []rpc.API   
	inprocHandler *rpc.Server 
	ipcEndpoint string       
	ipcListener net.Listener 
	ipcHandler  *rpc.Server  
	httpEndpoint     string       
	httpWhitelist    []string     
	httpListenerAddr net.Addr     
	httpServer       *http.Server 
	httpHandler      *rpc.Server  
	wsEndpoint     string       
	wsListenerAddr net.Addr     
	wsHTTPServer   *http.Server 
	wsHandler      *rpc.Server  
	stop chan struct{} 
	lock sync.RWMutex
	log log.Logger
}
func New(conf *Config) (*Node, error) {
	confCopy := *conf
	conf = &confCopy
	if conf.DataDir != "" {
		absdatadir, err := filepath.Abs(conf.DataDir)
		if err != nil {
			return nil, err
		}
		conf.DataDir = absdatadir
	}
	if strings.ContainsAny(conf.Name, `/\`) {
		return nil, errors.New(`Config.Name must not contain '/' or '\'`)
	}
	if conf.Name == datadirDefaultKeyStore {
		return nil, errors.New(`Config.Name cannot be "` + datadirDefaultKeyStore + `"`)
	}
	if strings.HasSuffix(conf.Name, ".ipc") {
		return nil, errors.New(`Config.Name cannot end in ".ipc"`)
	}
	am, ephemeralKeystore, err := makeAccountManager(conf)
	if err != nil {
		return nil, err
	}
	if conf.Logger == nil {
		conf.Logger = log.New()
	}
	return &Node{
		accman:            am,
		ephemeralKeystore: ephemeralKeystore,
		config:            conf,
		serviceFuncs:      []ServiceConstructor{},
		ipcEndpoint:       conf.IPCEndpoint(),
		httpEndpoint:      conf.HTTPEndpoint(),
		wsEndpoint:        conf.WSEndpoint(),
		eventmux:          new(event.TypeMux),
		log:               conf.Logger,
	}, nil
}
func (n *Node) Close() error {
	var errs []error
	if err := n.Stop(); err != nil && err != ErrNodeStopped {
		errs = append(errs, err)
	}
	if err := n.accman.Close(); err != nil {
		errs = append(errs, err)
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%v", errs)
	}
}
func (n *Node) Register(constructor ServiceConstructor) error {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.server != nil {
		return ErrNodeRunning
	}
	n.serviceFuncs = append(n.serviceFuncs, constructor)
	return nil
}
func (n *Node) Start() error {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.server != nil {
		return ErrNodeRunning
	}
	if err := n.openDataDir(); err != nil {
		return err
	}
	n.serverConfig = n.config.P2P
	n.serverConfig.PrivateKey = n.config.NodeKey()
	n.serverConfig.Name = n.config.NodeName()
	n.serverConfig.Logger = n.log
	if n.serverConfig.StaticNodes == nil {
		n.serverConfig.StaticNodes = n.config.StaticNodes()
	}
	if n.serverConfig.TrustedNodes == nil {
		n.serverConfig.TrustedNodes = n.config.TrustedNodes()
	}
	if n.serverConfig.NodeDatabase == "" {
		n.serverConfig.NodeDatabase = n.config.NodeDB()
	}
	running := &p2p.Server{Config: n.serverConfig}
	n.log.Info("Starting peer-to-peer node", "instance", n.serverConfig.Name)
	services := make(map[reflect.Type]Service)
	for _, constructor := range n.serviceFuncs {
		ctx := &ServiceContext{
			Config:         *n.config,
			services:       make(map[reflect.Type]Service),
			EventMux:       n.eventmux,
			AccountManager: n.accman,
		}
		for kind, s := range services { 
			ctx.services[kind] = s
		}
		service, err := constructor(ctx)
		if err != nil {
			return err
		}
		kind := reflect.TypeOf(service)
		if _, exists := services[kind]; exists {
			return &DuplicateServiceError{Kind: kind}
		}
		services[kind] = service
	}
	for _, service := range services {
		running.Protocols = append(running.Protocols, service.Protocols()...)
	}
	if err := running.Start(); err != nil {
		return convertFileLockError(err)
	}
	var started []reflect.Type
	for kind, service := range services {
		if err := service.Start(running); err != nil {
			for _, kind := range started {
				services[kind].Stop()
			}
			running.Stop()
			return err
		}
		started = append(started, kind)
	}
	if err := n.startRPC(services); err != nil {
		for _, service := range services {
			service.Stop()
		}
		running.Stop()
		return err
	}
	n.services = services
	n.server = running
	n.stop = make(chan struct{})
	return nil
}
func (n *Node) Config() *Config {
	return n.config
}
func (n *Node) openDataDir() error {
	if n.config.DataDir == "" {
		return nil 
	}
	instdir := filepath.Join(n.config.DataDir, n.config.name())
	if err := os.MkdirAll(instdir, 0700); err != nil {
		return err
	}
	release, _, err := fileutil.Flock(filepath.Join(instdir, "LOCK"))
	if err != nil {
		return convertFileLockError(err)
	}
	n.instanceDirLock = release
	return nil
}
func (n *Node) startRPC(services map[reflect.Type]Service) error {
	apis := n.apis()
	for _, service := range services {
		apis = append(apis, service.APIs()...)
	}
	if err := n.startInProc(apis); err != nil {
		return err
	}
	if err := n.startIPC(apis); err != nil {
		n.stopInProc()
		return err
	}
	if err := n.startHTTP(n.httpEndpoint, apis, n.config.HTTPModules, n.config.HTTPCors, n.config.HTTPVirtualHosts, n.config.HTTPTimeouts, n.config.WSOrigins); err != nil {
		n.stopIPC()
		n.stopInProc()
		return err
	}
	if n.httpEndpoint != n.wsEndpoint {
		if err := n.startWS(n.wsEndpoint, apis, n.config.WSModules, n.config.WSOrigins, n.config.WSExposeAll); err != nil {
			n.stopHTTP()
			n.stopIPC()
			n.stopInProc()
			return err
		}
	}
	n.rpcAPIs = apis
	return nil
}
func (n *Node) startInProc(apis []rpc.API) error {
	handler := rpc.NewServer()
	for _, api := range apis {
		if err := handler.RegisterName(api.Namespace, api.Service); err != nil {
			return err
		}
		n.log.Debug("InProc registered", "namespace", api.Namespace)
	}
	n.inprocHandler = handler
	return nil
}
func (n *Node) stopInProc() {
	if n.inprocHandler != nil {
		n.inprocHandler.Stop()
		n.inprocHandler = nil
	}
}
func (n *Node) startIPC(apis []rpc.API) error {
	if n.ipcEndpoint == "" {
		return nil 
	}
	listener, handler, err := rpc.StartIPCEndpoint(n.ipcEndpoint, apis)
	if err != nil {
		return err
	}
	n.ipcListener = listener
	n.ipcHandler = handler
	n.log.Info("IPC endpoint opened", "url", n.ipcEndpoint)
	return nil
}
func (n *Node) stopIPC() {
	if n.ipcListener != nil {
		n.ipcListener.Close()
		n.ipcListener = nil
		n.log.Info("IPC endpoint closed", "url", n.ipcEndpoint)
	}
	if n.ipcHandler != nil {
		n.ipcHandler.Stop()
		n.ipcHandler = nil
	}
}
func (n *Node) startHTTP(endpoint string, apis []rpc.API, modules []string, cors []string, vhosts []string, timeouts rpc.HTTPTimeouts, wsOrigins []string) error {
	if endpoint == "" {
		return nil
	}
	srv := rpc.NewServer()
	err := RegisterApisFromWhitelist(apis, modules, srv, false)
	if err != nil {
		return err
	}
	handler := NewHTTPHandlerStack(srv, cors, vhosts)
	if n.httpEndpoint == n.wsEndpoint {
		handler = NewWebsocketUpgradeHandler(handler, srv.WebsocketHandler(wsOrigins))
	}
	httpServer, addr, err := StartHTTPEndpoint(endpoint, timeouts, handler)
	if err != nil {
		return err
	}
	n.log.Info("HTTP endpoint opened", "url", fmt.Sprintf("http:
		"cors", strings.Join(cors, ","),
		"vhosts", strings.Join(vhosts, ","))
	if n.httpEndpoint == n.wsEndpoint {
		n.log.Info("WebSocket endpoint opened", "url", fmt.Sprintf("ws:
	}
	n.httpEndpoint = endpoint
	n.httpListenerAddr = addr
	n.httpServer = httpServer
	n.httpHandler = srv
	return nil
}
func (n *Node) stopHTTP() {
	if n.httpServer != nil {
		n.httpServer.Shutdown(context.Background())
		n.log.Info("HTTP endpoint closed", "url", fmt.Sprintf("http:
	}
	if n.httpHandler != nil {
		n.httpHandler.Stop()
		n.httpHandler = nil
	}
}
func (n *Node) startWS(endpoint string, apis []rpc.API, modules []string, wsOrigins []string, exposeAll bool) error {
	if endpoint == "" {
		return nil
	}
	srv := rpc.NewServer()
	handler := srv.WebsocketHandler(wsOrigins)
	err := RegisterApisFromWhitelist(apis, modules, srv, exposeAll)
	if err != nil {
		return err
	}
	httpServer, addr, err := startWSEndpoint(endpoint, handler)
	if err != nil {
		return err
	}
	n.log.Info("WebSocket endpoint opened", "url", fmt.Sprintf("ws:
	n.wsEndpoint = endpoint
	n.wsListenerAddr = addr
	n.wsHTTPServer = httpServer
	n.wsHandler = srv
	return nil
}
func (n *Node) stopWS() {
	if n.wsHTTPServer != nil {
		n.wsHTTPServer.Shutdown(context.Background())
		n.log.Info("WebSocket endpoint closed", "url", fmt.Sprintf("ws:
	}
	if n.wsHandler != nil {
		n.wsHandler.Stop()
		n.wsHandler = nil
	}
}
func (n *Node) Stop() error {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.server == nil {
		return ErrNodeStopped
	}
	n.stopWS()
	n.stopHTTP()
	n.stopIPC()
	n.rpcAPIs = nil
	failure := &StopError{
		Services: make(map[reflect.Type]error),
	}
	for kind, service := range n.services {
		if err := service.Stop(); err != nil {
			failure.Services[kind] = err
		}
	}
	n.server.Stop()
	n.services = nil
	n.server = nil
	if n.instanceDirLock != nil {
		if err := n.instanceDirLock.Release(); err != nil {
			n.log.Error("Can't release datadir lock", "err", err)
		}
		n.instanceDirLock = nil
	}
	close(n.stop)
	var keystoreErr error
	if n.ephemeralKeystore != "" {
		keystoreErr = os.RemoveAll(n.ephemeralKeystore)
	}
	if len(failure.Services) > 0 {
		return failure
	}
	if keystoreErr != nil {
		return keystoreErr
	}
	return nil
}
func (n *Node) Wait() {
	n.lock.RLock()
	if n.server == nil {
		n.lock.RUnlock()
		return
	}
	stop := n.stop
	n.lock.RUnlock()
	<-stop
}
func (n *Node) Restart() error {
	if err := n.Stop(); err != nil {
		return err
	}
	if err := n.Start(); err != nil {
		return err
	}
	return nil
}
func (n *Node) Attach() (*rpc.Client, error) {
	n.lock.RLock()
	defer n.lock.RUnlock()
	if n.server == nil {
		return nil, ErrNodeStopped
	}
	return rpc.DialInProc(n.inprocHandler), nil
}
func (n *Node) RPCHandler() (*rpc.Server, error) {
	n.lock.RLock()
	defer n.lock.RUnlock()
	if n.inprocHandler == nil {
		return nil, ErrNodeStopped
	}
	return n.inprocHandler, nil
}
func (n *Node) Server() *p2p.Server {
	n.lock.RLock()
	defer n.lock.RUnlock()
	return n.server
}
func (n *Node) Service(service interface{}) error {
	n.lock.RLock()
	defer n.lock.RUnlock()
	if n.server == nil {
		return ErrNodeStopped
	}
	element := reflect.ValueOf(service).Elem()
	if running, ok := n.services[element.Type()]; ok {
		element.Set(reflect.ValueOf(running))
		return nil
	}
	return ErrServiceUnknown
}
func (n *Node) DataDir() string {
	return n.config.DataDir
}
func (n *Node) InstanceDir() string {
	return n.config.instanceDir()
}
func (n *Node) AccountManager() *accounts.Manager {
	return n.accman
}
func (n *Node) IPCEndpoint() string {
	return n.ipcEndpoint
}
func (n *Node) HTTPEndpoint() string {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.httpListenerAddr != nil {
		return n.httpListenerAddr.String()
	}
	return n.httpEndpoint
}
func (n *Node) WSEndpoint() string {
	n.lock.Lock()
	defer n.lock.Unlock()
	if n.wsListenerAddr != nil {
		return n.wsListenerAddr.String()
	}
	return n.wsEndpoint
}
func (n *Node) EventMux() *event.TypeMux {
	return n.eventmux
}
func (n *Node) OpenDatabase(name string, cache, handles int, namespace string) (ethdb.Database, error) {
	if n.config.DataDir == "" {
		return rawdb.NewMemoryDatabase(), nil
	}
	return rawdb.NewLevelDBDatabase(n.config.ResolvePath(name), cache, handles, namespace)
}
func (n *Node) OpenDatabaseWithFreezer(name string, cache, handles int, freezer, namespace string) (ethdb.Database, error) {
	if n.config.DataDir == "" {
		return rawdb.NewMemoryDatabase(), nil
	}
	root := n.config.ResolvePath(name)
	switch {
	case freezer == "":
		freezer = filepath.Join(root, "ancient")
	case !filepath.IsAbs(freezer):
		freezer = n.config.ResolvePath(freezer)
	}
	return rawdb.NewLevelDBDatabaseWithFreezer(root, cache, handles, freezer, namespace)
}
func (n *Node) ResolvePath(x string) string {
	return n.config.ResolvePath(x)
}
func (n *Node) apis() []rpc.API {
	return []rpc.API{
		{
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPrivateAdminAPI(n),
		}, {
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPublicAdminAPI(n),
			Public:    true,
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   debug.Handler,
		}, {
			Namespace: "web3",
			Version:   "1.0",
			Service:   NewPublicWeb3API(n),
			Public:    true,
		},
	}
}
func RegisterApisFromWhitelist(apis []rpc.API, modules []string, srv *rpc.Server, exposeAll bool) error {
	if bad, available := checkModuleAvailability(modules, apis); len(bad) > 0 {
		log.Error("Unavailable modules in HTTP API list", "unavailable", bad, "available", available)
	}
	whitelist := make(map[string]bool)
	for _, module := range modules {
		whitelist[module] = true
	}
	for _, api := range apis {
		if exposeAll || whitelist[api.Namespace] || (len(whitelist) == 0 && api.Public) {
			if err := srv.RegisterName(api.Namespace, api.Service); err != nil {
				return err
			}
		}
	}
	return nil
}
