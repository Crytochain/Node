package node
import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"github.com/Cryptochain-VON/p2p"
	"github.com/Cryptochain-VON/p2p/nat"
	"github.com/Cryptochain-VON/rpc"
)
const (
	DefaultHTTPHost    = "localhost" 
	DefaultHTTPPort    = 8545        
	DefaultWSHost      = "localhost" 
	DefaultWSPort      = 8546        
	DefaultGraphQLHost = "localhost" 
	DefaultGraphQLPort = 8547        
)
var DefaultConfig = Config{
	DataDir:             DefaultDataDir(),
	HTTPPort:            DefaultHTTPPort,
	HTTPModules:         []string{"net", "web3"},
	HTTPVirtualHosts:    []string{"localhost"},
	HTTPTimeouts:        rpc.DefaultHTTPTimeouts,
	WSPort:              DefaultWSPort,
	WSModules:           []string{"net", "web3"},
	GraphQLPort:         DefaultGraphQLPort,
	GraphQLVirtualHosts: []string{"localhost"},
	P2P: p2p.Config{
		ListenAddr: ":30303",
		MaxPeers:   50,
		NAT:        nat.Any(),
	},
}
func DefaultDataDir() string {
	home := homeDir()
	if home != "" {
		switch runtime.GOOS {
		case "darwin":
			return filepath.Join(home, "Library", "Ethereum")
		case "windows":
			fallback := filepath.Join(home, "AppData", "Roaming", "Ethereum")
			appdata := windowsAppData()
			if appdata == "" || isNonEmptyDir(fallback) {
				return fallback
			}
			return filepath.Join(appdata, "Ethereum")
		default:
			return filepath.Join(home, ".ethereum")
		}
	}
	return ""
}
func windowsAppData() string {
	v := os.Getenv("LOCALAPPDATA")
	if v == "" {
		panic("environment variable LocalAppData is undefined")
	}
	return v
}
func isNonEmptyDir(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	names, _ := f.Readdir(1)
	f.Close()
	return len(names) > 0
}
func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if usr, err := user.Current(); err == nil {
		return usr.HomeDir
	}
	return ""
}
