package torr

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/anacrolix/publicip"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/wlynxg/anet"

	"server/settings"
	"server/torr/storage/torrstor"
	"server/torr/utils"
	"server/version"
)

type BTServer struct {
	config *torrent.ClientConfig
	client *torrent.Client

	storage *torrstor.Storage

	torrents map[metainfo.Hash]*Torrent

	mu sync.RWMutex
	// connectMu serializes Connect/Disconnect, which sleep while the old uTP
	// socket is released. It is separate from mu so that readers (/stat,
	// /torrents, streams) never wait for that sleep (KI-020).
	connectMu sync.Mutex
}

var privateIPBlocks []*net.IPNet

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // RFC3927 link-local
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local addr
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("parse error on %q: %v", cidr, err))
		}
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

func NewBTS() *BTServer {
	bts := new(BTServer)
	bts.torrents = make(map[metainfo.Hash]*Torrent)
	return bts
}

func (bt *BTServer) Connect() error {
	utils.PrefetchTrackers()

	// connectMu, not bt.mu: the retry sleeps for as long as the old uTP socket is
	// released (up to connectAttempts*connectPause), and holding bt.mu through
	// that blocked every /stat, /torrents and stream request (KI-020).
	bt.connectMu.Lock()
	defer bt.connectMu.Unlock()

	bt.mu.Lock()
	// Idempotent: a second Connect (two settings saves racing, a manual retry)
	// must not overwrite a live client and abandon its storage.
	if bt.client != nil {
		bt.client.Close()
		bt.client = nil
	}
	bt.closeStorageLocked()
	// configure() builds the config AND creates the storage, which starts a
	// worker and a full cache scan. It runs once per Connect: retrying it per
	// attempt created and abandoned a storage each time (KI-020: 12 scans in the
	// 16 s reconnect window on prod).
	bt.configure()
	cfg := bt.config
	bt.mu.Unlock()

	client, err := retryClient(connectAttempts, connectPause, func() (*torrent.Client, error) {
		return torrent.NewClient(cfg)
	})

	bt.mu.Lock()
	defer bt.mu.Unlock()
	if err != nil {
		// Nothing will use this storage: stop its worker instead of leaving it
		// behind for the process lifetime.
		bt.closeStorageLocked()
		return err
	}
	bt.client = client
	bt.torrents = make(map[metainfo.Hash]*Torrent)
	InitApiHelper(bt)

	return nil
}

// connectAttempts and connectPause bound the client-creation retry. The uTP
// socket of a closed client is released lazily, only after its peers are gone,
// and until then PeersListenPort is busy. On prod the release took ~13 s, and
// the old 1 s pause rounded that up further; 250 ms polling finds the free
// moment without the quantisation (KI-020).
var (
	connectAttempts = 60
	connectPause    = 250 * time.Millisecond
)

// retryClient holds the retry policy in one testable place: it returns the
// client as soon as creation succeeds, otherwise the last error after attempts.
func retryClient(attempts int, pause time.Duration, newClient func() (*torrent.Client, error)) (*torrent.Client, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var c *torrent.Client
		c, err = newClient()
		if err == nil {
			if i > 0 {
				log.Println("connect ok, attempt", i+1)
			}
			return c, nil
		}
		if i == 0 {
			log.Println("connect error, retrying:", err)
		}
		if i < attempts-1 {
			time.Sleep(pause)
		}
	}
	log.Println("connect failed:", err)
	return nil, err
}

// closeStorageLocked stops the storage's worker and drops the reference.
// Callers hold bt.mu.
func (bt *BTServer) closeStorageLocked() {
	if bt.storage != nil {
		_ = bt.storage.Close()
		bt.storage = nil
	}
}

func (bt *BTServer) Disconnect() {
	// Same lock as Connect: a save must not close the storage a Connect is
	// building, and the caller order in SetSettings is Disconnect -> Connect.
	bt.connectMu.Lock()
	defer bt.connectMu.Unlock()

	bt.mu.Lock()
	defer bt.mu.Unlock()
	if bt.client != nil {
		bt.client.Close()
		bt.client = nil
	}
	// The storage owns a background worker, its counters and the piece file
	// pool. Leaving it behind leaked one goroutine and one periodic cache scan
	// per settings save (KI-020: goroutines 4 -> 5 -> 6 for three saves).
	// Callers on the settings path drop the torrents first, so nothing reads
	// these caches any more; Shutdown exits right after.
	bt.closeStorageLocked()
	utils.FreeOSMemGC()
}

// publicIPTimeout bounds the public address lookups. Without it Connect blocked
// for as long as the lookup host was unreachable, which is startup and every
// settings save.
var publicIPTimeout = 3 * time.Second

// publicIPLookup is publicip.Get4/Get6 behind a seam: tests replace it to prove
// the lookup is cancelled by the deadline instead of hanging Connect.
var publicIPLookup = func(ctx context.Context, v6 bool) (net.IP, error) {
	if v6 {
		return publicip.Get6(ctx)
	}
	return publicip.Get4(ctx)
}

// publicIPContext is the deadline shared by both lookups, so a box whose IPv4
// and IPv6 lookups both hang waits publicIPTimeout, not twice that.
func publicIPContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), publicIPTimeout)
}

// resolvePublicIP runs one lookup under the caller's deadline.
func resolvePublicIP(ctx context.Context, v6 bool) (net.IP, error) {
	return publicIPLookup(ctx, v6)
}

func (bt *BTServer) configure() {
	blocklist, _ := utils.ReadBlockedIP()
	bt.config = torrent.NewDefaultClientConfig()

	if settings.BTsets.EnableLPD {
		bt.config.LocalServiceDiscovery = &torrent.LocalServiceDiscoveryConfig{
			Ip6: settings.BTsets.LPDIPv6 && settings.BTsets.EnableIPv6,
		}
	} else {
		bt.config.LocalServiceDiscovery = nil
	}

	bt.storage = torrstor.NewStorage(settings.BTsets.CacheSize)
	bt.config.DefaultStorage = bt.storage

	userAgent := "qBittorrent/4.3.9"
	peerID := "-qB4390-"
	upnpID := "TorrServer/" + version.Version
	cliVers := userAgent

	bt.config.Debug = settings.BTsets.EnableDebug
	bt.config.DisableIPv6 = !settings.BTsets.EnableIPv6
	bt.config.DisableTCP = settings.BTsets.DisableTCP
	bt.config.DisableUTP = settings.BTsets.DisableUTP
	//	https://github.com/anacrolix/torrent/issues/703
	// bt.config.DisableWebtorrent = true //	NE
	// bt.config.DisableWebseeds = false  //	NE
	bt.config.NoDefaultPortForwarding = settings.BTsets.DisableUPNP
	bt.config.NoDHT = settings.BTsets.DisableDHT
	bt.config.DisablePEX = settings.BTsets.DisablePEX
	bt.config.NoUpload = settings.BTsets.DisableUpload
	bt.config.IPBlocklist = blocklist
	bt.config.Bep20 = peerID
	bt.config.PeerID = utils.PeerIDRandom(peerID)
	bt.config.UpnpID = upnpID
	bt.config.HTTPUserAgent = userAgent
	bt.config.ExtendedHandshakeClientVersion = cliVers
	bt.config.EstablishedConnsPerTorrent = settings.BTsets.ConnectionsLimit
	bt.config.TotalHalfOpenConns = 500
	// RC4 stream encryption is negotiated per connection even when it is not
	// forced, and while downloading it was 17-19% of process CPU (measured:
	// crypto/rc4.XORKeyStream, two profiles on separate torrents). It only
	// hides BitTorrent traffic from an ISP's shaping, which matters for a
	// client and not for a LAN media server, so it can be refused outright.
	bt.config.EncryptionPolicy = encryptionPolicy(settings.BTsets.ForceEncrypt, settings.BTsets.DisableEncrypt)
	// bt.config.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{ //	NE
	// 	RequirePreferred: settings.BTsets.ForceEncrypt, //	NE
	// 	Preferred:        true,                         //	NE
	// } //	NE
	if settings.BTsets.DownloadRateLimit > 0 {
		bt.config.DownloadRateLimiter = utils.Limit(settings.BTsets.DownloadRateLimit * 1024)
	}
	if settings.BTsets.UploadRateLimit > 0 {
		bt.config.Seed = true
		bt.config.UploadRateLimiter = utils.Limit(settings.BTsets.UploadRateLimit * 1024)
	}
	if settings.TorAddr != "" {
		log.Println("Set listen addr", settings.TorAddr)
		bt.config.SetListenAddr(settings.TorAddr)
	} else {
		if settings.BTsets.PeersListenPort > 0 {
			log.Println("Set listen port", settings.BTsets.PeersListenPort)
			bt.config.ListenPort = settings.BTsets.PeersListenPort
		} else {
			log.Println("Set listen port to random autoselect (0)")
			bt.config.ListenPort = 0
		}
	}

	// Configure proxy if enabled
	if err := bt.configureProxy(); err != nil {
		log.Println("Proxy configuration error:", err)
	}

	log.Println("Client config:", settings.BTsets)

	var err error

	// set public IPv4
	ipCtx, cancelIP := publicIPContext()
	defer cancelIP()
	if settings.PubIPv4 != "" {
		if ip4 := net.ParseIP(settings.PubIPv4); ip4.To4() != nil && !isPrivateIP(ip4) {
			bt.config.PublicIp4 = ip4
		}
	}
	if bt.config.PublicIp4 == nil {
		bt.config.PublicIp4, err = resolvePublicIP(ipCtx, false)
		if err != nil {
			log.Printf("error getting public ipv4 address: %v", err)
		}
	}
	if bt.config.PublicIp4.To4() == nil { // possible IPv6 from publicip.Get4(ctx)
		bt.config.PublicIp4 = nil
	}
	if bt.config.PublicIp4 != nil {
		log.Println("PublicIp4:", bt.config.PublicIp4)
	}

	// set public IPv6
	if settings.PubIPv6 != "" {
		if ip6 := net.ParseIP(settings.PubIPv6); ip6.To16() != nil && ip6.To4() == nil && !isPrivateIP(ip6) {
			bt.config.PublicIp6 = ip6
		}
	}
	if bt.config.PublicIp6 == nil && settings.BTsets.EnableIPv6 {
		bt.config.PublicIp6, err = resolvePublicIP(ipCtx, true)
		if err != nil {
			log.Printf("error getting public ipv6 address: %v", err)
		}
	}
	if bt.config.PublicIp6.To16() == nil { // just 4 sure it's valid IPv6
		bt.config.PublicIp6 = nil
	}
	if bt.config.PublicIp6 != nil {
		log.Println("PublicIp6:", bt.config.PublicIp6)
	}
}

// encryptionPolicy resolves the two contradicting encryption settings into one
// anacrolix policy. Split out of configure so the rule can be tested without a
// BT client.
//
// ForceEncrypt wins over DisableEncrypt: if both were honoured the policy
// would be self-contradictory, and picking one silently would make the other
// setting a lie in the UI.
func encryptionPolicy(force, disable bool) torrent.EncryptionPolicy {
	return torrent.EncryptionPolicy{
		DisableEncryption: disable,
		ForceEncryption:   force && !disable,
	}
}

func (bt *BTServer) configureProxy() error {
	proxyURL := settings.Args.ProxyURL

	if proxyURL == "" {
		return nil // No proxy configured
	}

	proxyMode := settings.Args.ProxyMode
	if proxyMode == "" {
		proxyMode = "tracker" // default
	}

	// Parse and validate proxy URL
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %w", err)
	}

	scheme := parsedURL.Scheme
	// Validate proxy protocol
	switch scheme {
	case "socks5", "socks5h", "socks4", "socks4a", "http", "https":
		// Supported protocols
	default:
		return fmt.Errorf("unsupported proxy protocol: %s (supported: http, https, socks4, socks4a, socks5, socks5h)", scheme)
	}

	if proxyMode == "full" {
		log.Printf("Configuring proxy for all BitTorrent traffic: %s://%s", scheme, parsedURL.Host)

		// Set ProxyURL - this will be used by anacrolix/torrent for all BitTorrent traffic
		bt.config.ProxyURL = proxyURL

		// Also set HTTPProxy explicitly for HTTP tracker requests
		bt.config.HTTPProxy = func(req *http.Request) (*url.URL, error) {
			return parsedURL, nil
		}

		log.Println("Proxy configured successfully for all BitTorrent connections (tracker, DHT, peers)")
	} else if proxyMode == "peers" {
		log.Printf("Configuring proxy for peer connections only: %s://%s", scheme, parsedURL.Host)

		// Set ProxyURL for peer connections, but don't set HTTPProxy
		// This routes DHT and peer connections through proxy, but not HTTP tracker requests
		bt.config.ProxyURL = proxyURL

		log.Println("Proxy configured successfully for peer and DHT connections only")
	} else {
		log.Printf("Configuring proxy for HTTP tracker requests only: %s://%s", scheme, parsedURL.Host)

		// Only set HTTPProxy for tracker requests, don't set ProxyURL
		bt.config.HTTPProxy = func(req *http.Request) (*url.URL, error) {
			return parsedURL, nil
		}

		log.Println("Proxy configured successfully for HTTP tracker connections only")
	}

	return nil
}

func (bt *BTServer) GetTorrent(hash torrent.InfoHash) *Torrent {
	bt.mu.RLock()
	torr := bt.torrents[hash]
	bt.mu.RUnlock()
	return torr
}

func (bt *BTServer) ListTorrents() map[metainfo.Hash]*Torrent {
	list := make(map[metainfo.Hash]*Torrent)
	bt.mu.RLock()
	maps.Copy(list, bt.torrents)
	bt.mu.RUnlock()
	return list
}

func (bt *BTServer) RemoveTorrent(hash torrent.InfoHash) bool {
	// Torrent.Close takes bt.mu, so the lock must be released before calling it
	if torr := bt.GetTorrent(hash); torr != nil {
		return torr.Close()
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

func getPublicIp4() net.IP {
	ifaces, err := anet.Interfaces()
	if err != nil {
		log.Println("Error get public IPv4")
		return nil
	}
	for _, i := range ifaces {
		addrs, _ := anet.InterfaceAddrsByInterface(&i)
		if i.Flags&net.FlagUp == net.FlagUp {
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if !isPrivateIP(ip) && ip.To4() != nil {
					return ip
				}
			}
		}
	}
	return nil
}

func getPublicIp6() net.IP {
	ifaces, err := anet.Interfaces()
	if err != nil {
		log.Println("Error get public IPv6")
		return nil
	}
	for _, i := range ifaces {
		addrs, _ := anet.InterfaceAddrsByInterface(&i)
		if i.Flags&net.FlagUp == net.FlagUp {
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if !isPrivateIP(ip) && ip.To16() != nil && ip.To4() == nil {
					return ip
				}
			}
		}
	}
	return nil
}
