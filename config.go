package node
import (
	"crypto/ecdsa"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"github.com/Cryptochain-VON/accounts"
	"github.com/Cryptochain-VON/accounts/external"
	"github.com/Cryptochain-VON/accounts/keystore"
	"github.com/Cryptochain-VON/accounts/scwallet"
	"github.com/Cryptochain-VON/accounts/usbwallet"
	"github.com/Cryptochain-VON/common"
	"github.com/Cryptochain-VON/crypto"
	"github.com/Cryptochain-VON/log"
	"github.com/Cryptochain-VON/p2p"
	"github.com/Cryptochain-VON/p2p/enode"
	"github.com/Cryptochain-VON/rpc"
)
const (
	datadirPrivateKey      = "nodekey"            
	datadirDefaultKeyStore = "keystore"           
	datadirStaticNodes     = "static-nodes.json"  
	datadirTrustedNodes    = "trusted-nodes.json" 
	datadirNodeDatabase    = "nodes"              
)
type Config struct {
	Name string `toml:"-"`
	UserIdent string `toml:",omitempty"`
	Version string `toml:"-"`
	DataDir string
	P2P p2p.Config
	KeyStoreDir string `toml:",omitempty"`
	ExternalSigner string `toml:",omitempty"`
	UseLightweightKDF bool `toml:",omitempty"`
	InsecureUnlockAllowed bool `toml:",omitempty"`
	NoUSB bool `toml:",omitempty"`
	SmartCardDaemonPath string `toml:",omitempty"`
	IPCPath string `toml:",omitempty"`
	HTTPHost string `toml:",omitempty"`
	HTTPPort int `toml:",omitempty"`
	HTTPCors []string `toml:",omitempty"`
	HTTPVirtualHosts []string `toml:",omitempty"`
	HTTPModules []string `toml:",omitempty"`
	HTTPTimeouts rpc.HTTPTimeouts
	WSHost string `toml:",omitempty"`
	WSPort int `toml:",omitempty"`
	WSOrigins []string `toml:",omitempty"`
	WSModules []string `toml:",omitempty"`
	WSExposeAll bool `toml:",omitempty"`
	GraphQLHost string `toml:",omitempty"`
	GraphQLPort int `toml:",omitempty"`
	GraphQLCors []string `toml:",omitempty"`
	GraphQLVirtualHosts []string `toml:",omitempty"`
	Logger log.Logger `toml:",omitempty"`
	staticNodesWarning     bool
	trustedNodesWarning    bool
	oldGethResourceWarning bool
}
func (c *Config) IPCEndpoint() string {
	if c.IPCPath == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(c.IPCPath, `\\.\pipe\`) {
			return c.IPCPath
		}
		return `\\.\pipe\` + c.IPCPath
	}
	if filepath.Base(c.IPCPath) == c.IPCPath {
		if c.DataDir == "" {
			return filepath.Join(os.TempDir(), c.IPCPath)
		}
		return filepath.Join(c.DataDir, c.IPCPath)
	}
	return c.IPCPath
}
func (c *Config) NodeDB() string {
	if c.DataDir == "" {
		return "" 
	}
	return c.ResolvePath(datadirNodeDatabase)
}
func DefaultIPCEndpoint(clientIdentifier string) string {
	if clientIdentifier == "" {
		clientIdentifier = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if clientIdentifier == "" {
			panic("empty executable name")
		}
	}
	config := &Config{DataDir: DefaultDataDir(), IPCPath: clientIdentifier + ".ipc"}
	return config.IPCEndpoint()
}
func (c *Config) HTTPEndpoint() string {
	if c.HTTPHost == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.HTTPHost, c.HTTPPort)
}
func (c *Config) GraphQLEndpoint() string {
	if c.GraphQLHost == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.GraphQLHost, c.GraphQLPort)
}
func DefaultHTTPEndpoint() string {
	config := &Config{HTTPHost: DefaultHTTPHost, HTTPPort: DefaultHTTPPort}
	return config.HTTPEndpoint()
}
func (c *Config) WSEndpoint() string {
	if c.WSHost == "" {
		return ""
	}
	return fmt.Sprintf("%s:%d", c.WSHost, c.WSPort)
}
func DefaultWSEndpoint() string {
	config := &Config{WSHost: DefaultWSHost, WSPort: DefaultWSPort}
	return config.WSEndpoint()
}
func (c *Config) ExtRPCEnabled() bool {
	return c.HTTPHost != "" || c.WSHost != "" || c.GraphQLHost != ""
}
func (c *Config) NodeName() string {
	name := c.name()
	if name == "geth" || name == "geth-testnet" {
		name = "Geth"
	}
	if c.UserIdent != "" {
		name += "/" + c.UserIdent
	}
	if c.Version != "" {
		name += "/v" + c.Version
	}
	name += "/" + runtime.GOOS + "-" + runtime.GOARCH
	name += "/" + runtime.Version()
	return name
}
func (c *Config) name() string {
	if c.Name == "" {
		progname := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
		if progname == "" {
			panic("empty executable name, set Config.Name")
		}
		return progname
	}
	return c.Name
}
var isOldGethResource = map[string]bool{
	"chaindata":          true,
	"nodes":              true,
	"nodekey":            true,
	"static-nodes.json":  false, 
	"trusted-nodes.json": false, 
}
func (c *Config) ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if c.DataDir == "" {
		return ""
	}
	if warn, isOld := isOldGethResource[path]; isOld {
		oldpath := ""
		if c.name() == "geth" {
			oldpath = filepath.Join(c.DataDir, path)
		}
		if oldpath != "" && common.FileExist(oldpath) {
			if warn {
				c.warnOnce(&c.oldGethResourceWarning, "Using deprecated resource file %s, please move this file to the 'geth' subdirectory of datadir.", oldpath)
			}
			return oldpath
		}
	}
	return filepath.Join(c.instanceDir(), path)
}
func (c *Config) instanceDir() string {
	if c.DataDir == "" {
		return ""
	}
	return filepath.Join(c.DataDir, c.name())
}
func (c *Config) NodeKey() *ecdsa.PrivateKey {
	if c.P2P.PrivateKey != nil {
		return c.P2P.PrivateKey
	}
	if c.DataDir == "" {
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Crit(fmt.Sprintf("Failed to generate ephemeral node key: %v", err))
		}
		return key
	}
	keyfile := c.ResolvePath(datadirPrivateKey)
	if key, err := crypto.LoadECDSA(keyfile); err == nil {
		return key
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		log.Crit(fmt.Sprintf("Failed to generate node key: %v", err))
	}
	instanceDir := filepath.Join(c.DataDir, c.name())
	if err := os.MkdirAll(instanceDir, 0700); err != nil {
		log.Error(fmt.Sprintf("Failed to persist node key: %v", err))
		return key
	}
	keyfile = filepath.Join(instanceDir, datadirPrivateKey)
	if err := crypto.SaveECDSA(keyfile, key); err != nil {
		log.Error(fmt.Sprintf("Failed to persist node key: %v", err))
	}
	return key
}
func (c *Config) StaticNodes() []*enode.Node {
	return c.parsePersistentNodes(&c.staticNodesWarning, c.ResolvePath(datadirStaticNodes))
}
func (c *Config) TrustedNodes() []*enode.Node {
	return c.parsePersistentNodes(&c.trustedNodesWarning, c.ResolvePath(datadirTrustedNodes))
}
func (c *Config) parsePersistentNodes(w *bool, path string) []*enode.Node {
	if c.DataDir == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	c.warnOnce(w, "Found deprecated node list file %s, please use the TOML config file instead.", path)
	var nodelist []string
	if err := common.LoadJSON(path, &nodelist); err != nil {
		log.Error(fmt.Sprintf("Can't load node list file: %v", err))
		return nil
	}
	var nodes []*enode.Node
	for _, url := range nodelist {
		if url == "" {
			continue
		}
		node, err := enode.Parse(enode.ValidSchemes, url)
		if err != nil {
			log.Error(fmt.Sprintf("Node URL %s: %v\n", url, err))
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}
func (c *Config) AccountConfig() (int, int, string, error) {
	scryptN := keystore.StandardScryptN
	scryptP := keystore.StandardScryptP
	if c.UseLightweightKDF {
		scryptN = keystore.LightScryptN
		scryptP = keystore.LightScryptP
	}
	var (
		keydir string
		err    error
	)
	switch {
	case filepath.IsAbs(c.KeyStoreDir):
		keydir = c.KeyStoreDir
	case c.DataDir != "":
		if c.KeyStoreDir == "" {
			keydir = filepath.Join(c.DataDir, datadirDefaultKeyStore)
		} else {
			keydir, err = filepath.Abs(c.KeyStoreDir)
		}
	case c.KeyStoreDir != "":
		keydir, err = filepath.Abs(c.KeyStoreDir)
	}
	return scryptN, scryptP, keydir, err
}
func makeAccountManager(conf *Config) (*accounts.Manager, string, error) {
	scryptN, scryptP, keydir, err := conf.AccountConfig()
	var ephemeral string
	if keydir == "" {
		keydir, err = ioutil.TempDir("", "go-ethereum-keystore")
		ephemeral = keydir
	}
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(keydir, 0700); err != nil {
		return nil, "", err
	}
	var backends []accounts.Backend
	if len(conf.ExternalSigner) > 0 {
		log.Info("Using external signer", "url", conf.ExternalSigner)
		if extapi, err := external.NewExternalBackend(conf.ExternalSigner); err == nil {
			backends = append(backends, extapi)
		} else {
			return nil, "", fmt.Errorf("error connecting to external signer: %v", err)
		}
	}
	if len(backends) == 0 {
		backends = append(backends, keystore.NewKeyStore(keydir, scryptN, scryptP))
		if !conf.NoUSB {
			if ledgerhub, err := usbwallet.NewLedgerHub(); err != nil {
				log.Warn(fmt.Sprintf("Failed to start Ledger hub, disabling: %v", err))
			} else {
				backends = append(backends, ledgerhub)
			}
			if trezorhub, err := usbwallet.NewTrezorHubWithHID(); err != nil {
				log.Warn(fmt.Sprintf("Failed to start HID Trezor hub, disabling: %v", err))
			} else {
				backends = append(backends, trezorhub)
			}
			if trezorhub, err := usbwallet.NewTrezorHubWithWebUSB(); err != nil {
				log.Warn(fmt.Sprintf("Failed to start WebUSB Trezor hub, disabling: %v", err))
			} else {
				backends = append(backends, trezorhub)
			}
		}
		if len(conf.SmartCardDaemonPath) > 0 {
			if schub, err := scwallet.NewHub(conf.SmartCardDaemonPath, scwallet.Scheme, keydir); err != nil {
				log.Warn(fmt.Sprintf("Failed to start smart card hub, disabling: %v", err))
			} else {
				backends = append(backends, schub)
			}
		}
	}
	return accounts.NewManager(&accounts.Config{InsecureUnlockAllowed: conf.InsecureUnlockAllowed}, backends...), ephemeral, nil
}
var warnLock sync.Mutex
func (c *Config) warnOnce(w *bool, format string, args ...interface{}) {
	warnLock.Lock()
	defer warnLock.Unlock()
	if *w {
		return
	}
	l := c.Logger
	if l == nil {
		l = log.Root()
	}
	l.Warn(fmt.Sprintf(format, args...))
	*w = true
}
