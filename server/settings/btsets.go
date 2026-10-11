package settings

import (
	"encoding/json"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"strings"
	"time"

	"server/log"
)

type TorznabConfig struct {
	Host string
	Key  string
	Name string
}

type TMDBConfig struct {
	APIKey     string // TMDB API Key
	APIURL     string // Base API URL (default: https://api.themoviedb.org)
	ImageURL   string // Image URL (default: https://image.tmdb.org)
	ImageURLRu string // Image URL for Russian users (default: https://imagetmdb.com)
}

// Read-ahead window bounds, in MB. The window must stay a small fraction of
// the cache or eviction cannot bring the cache back under its limit.
const (
	MinReadAheadMB = 4
	MaxReadAheadMB = 1024
	// DefaultReadAheadMB is the *playing* window. A paused viewer gets
	// PausePreloadDefaultMB instead — see PausePreloadMB.
	DefaultReadAheadMB = 16
	// ViewerIdleGraceDefault — 30 минут. Столько окно зрителя остаётся
	// защищённым после последнего чтения (пауза, буферизация, фон).
	ViewerIdleGraceDefault = 1800
	ViewerIdleGraceMin     = 60

	// PausePreloadDefaultMB — окно для зрителя, поставленного на паузу.
	// В 20 раз больше игрового: пауза означает «дай буфер», а измеренное
	// поведение (90 МБ и тишина) этот запрос не выполняло.
	PausePreloadDefaultMB = 1024
	// PausePreloadAfterDefaultSec — сколько тишины считается паузой: мало,
	// чтобы намеренная пауза срабатывала, велико, чтобы обычная буферизация
	// не триггерила.
	PausePreloadAfterDefaultSec = 15

	// DefaultCacheStateTTLSec — 1 секунда. Меньше смысла нет: за это время
	// состояние кэша уже изменилось. Больше — UI заметно отстаёт.
	DefaultCacheStateTTLSec = 1
	// CacheStateTTLMaxSec — верхняя граница, чтобы опечатка в настройках не
	// превратила прогресс-бар в замороженный.
	CacheStateTTLMaxSec = 60
)

// CacheStateTTLDuration resolves BTSets.CacheStateTTL against the rules
// documented on the field: 0 = default, negative = disabled.
//
// It is a method on the struct rather than code at the call site so that the
// clamping exists exactly once — SetBTSets, loadBTSets and the readers all
// disagree about which values are legal otherwise.
func (v *BTSets) CacheStateTTLDuration() time.Duration {
	if v == nil || v.CacheStateTTL < 0 {
		return 0
	}
	ttl := v.CacheStateTTL
	if ttl == 0 {
		ttl = DefaultCacheStateTTLSec
	}
	if ttl > CacheStateTTLMaxSec {
		ttl = CacheStateTTLMaxSec
	}
	return time.Duration(ttl) * time.Second
}

type BTSets struct {
	// Cache
	CacheSize int64 // in byte, def 64 MB
	// ReaderReadAheadMB is how much data is kept buffered ahead of the
	// playback position, in MB. It also defines how much of the file around
	// the position is protected from eviction.
	//
	// It used to be "percent of CacheSize", which had no workable meaning:
	// at a 98 GiB cache, 81% meant an 80 GiB read-ahead window and 80 GiB of
	// cache that eviction was not allowed to touch (measured 2026-10-01). The
	// JSON key stays the same so existing configs keep working — old values
	// (5..100) land in the new range as 5..100 MB, which is a sane window.
	ReaderReadAheadMB int `json:"ReaderReadAHead"`
	// PausePreloadMB is the readahead window used once a viewer stops reading,
	// i.e. while the film is paused. 0 = keep ReaderReadAheadMB at all times.
	//
	// It exists because a paused viewer is a viewer asking for more buffer, and
	// the fixed window stopped exactly there: with ReaderReadAHead=90 MB the
	// server fetched 90 MB ahead of the playhead and then nothing, so watching
	// twenty seconds and pausing to build a buffer bought the same 90 MB every
	// time. Growing the window also grows getOffsetRange, so what it pulls is
	// protected from eviction for as long as the viewer stays idle.
	PausePreloadMB int `json:"PausePreloadMB"`
	// PausePreloadAfterSec is how long a reader must go without a read before
	// PausePreloadMB applies.
	PausePreloadAfterSec int     `json:"PausePreloadAfterSec"`
	PreloadCache         float64 // in percent, rounded to hundredths (0.01% - 100%)
	PreloadCacheMB       int64   // in MB, overrides PreloadCache percent if > 0 (allows e.g. 1MB at 100GB+ cache)
	OneCacheForAll       bool    // limit cache globally across all torrents instead of per-torrent
	CacheHysteresis      float64 // eviction threshold (0.50-1.0), default 0.90 = clean down to 90%
	// ReaderIdleGraceSec is how long a viewer's window stays protected from
	// eviction after its last read. (existing) ...
	ViewerIdleGraceSec int `json:"ViewerIdleGraceSec"`

	// CacheStateTTL is how long a computed `CacheState` may be reused for the
	// next identical request instead of being rebuilt from scratch, in seconds.
	// 0 = use DefaultCacheStateTTLSec, negative = disable (rebuild every time,
	// the behaviour before this setting existed).
	//
	// It exists because players poll `/cache` (and `/stream?...&stat`) far more
	// often than the state can change: measured on a 2-core box, 555 of the
	// last 600 requests in access.log were `/cache` from two clients, and each
	// one rebuilt the whole thing — a walk over every piece of a 28 GB torrent,
	// `PieceState()` under the client lock per piece, a zlib re-pack of the
	// torrs hash and a full re-serialisation of the result. That was 57% of
	// the process CPU. Reusing the snapshot for a second costs the UI a second
	// of freshness and nothing else.
	CacheStateTTL int `json:"CacheStateTTL"`

	// Disk
	UseDisk           bool
	TorrentsSavePath  string
	RemoveCacheOnDrop bool

	// Torrent
	ForceEncrypt             bool
	RetrackersMode           int    // 0 - don`t add, 1 - add retrackers (def), 2 - remove retrackers 3 - replace retrackers
	TrackersListURL          string // remote trackers list URL; empty = skip remote fetch
	DefaultTrackers          string // newline-separated announce URLs used as local/fallback list
	TorrentDisconnectTimeout int    // in seconds
	EnableDebug              bool   // debug logs

	// DLNA
	EnableDLNA bool
	// Bonjour/mDNS LAN discovery (_torrserver, _http, _https)
	EnableBonjour bool
	// Shared name for DLNA and Bonjour
	FriendlyName string

	// Rutor
	EnableRutorSearch bool

	// Torznab
	EnableTorznabSearch bool
	TorznabUrls         []TorznabConfig

	// TMDB
	TMDBSettings TMDBConfig

	// BT Config
	EnableIPv6        bool
	DisableTCP        bool
	DisableUTP        bool
	DisableUPNP       bool
	DisableDHT        bool
	DisablePEX        bool
	DisableUpload     bool
	DownloadRateLimit int // in kb, 0 - inf
	UploadRateLimit   int // in kb, 0 - inf
	ConnectionsLimit  int
	PeersListenPort   int

	// DisableEncrypt refuses the RC4 stream encryption peers ask for. It was
	// 17-19% of process CPU while downloading (crypto/rc4.XORKeyStream in the
	// production profile) purely because anacrolix accepts encryption whenever
	// a peer offers it. The trade is that an ISP can shape the traffic, which
	// matters for a desktop client and not for a LAN media server.
	//
	// ForceEncrypt wins if both are set: they contradict each other.
	DisableEncrypt bool

	// LPD
	EnableLPD bool
	LPDIPv6   bool

	// HTTPS
	SslPort int
	SslCert string
	SslKey  string

	// Reader
	ResponsiveMode bool // enable Responsive reader (don't wait pieceComplete)

	// FS
	ShowFSActiveTorr bool

	// Storage preferences
	StoreSettingsInJson bool
	StoreViewedInJson   bool

	// Viewed timecodes
	TrackTimecode bool // store playback position (timecode) in viewed data
}

func (v *BTSets) String() string {
	buf, _ := json.Marshal(v)
	return string(buf)
}

// Default remote trackers list and built-in announce URLs (also used by Web UI defaults).
const DefaultTrackersListURL = "https://raw.githubusercontent.com/ngosang/trackerslist/master/trackers_best_ip.txt"

const DefaultTrackersText = `http://retracker.local/announce
http://bt4.t-ru.org/ann?magnet
http://retracker.mgts.by:80/announce
http://tracker.city9x.com:2710/announce
http://tracker.electro-torrent.pl:80/announce
http://tracker.internetwarriors.net:1337/announce
http://tracker2.itzmx.com:6961/announce
udp://opentor.org:2710
udp://public.popcorn-tracker.org:6969/announce
udp://tracker.opentrackr.org:1337/announce
http://bt.svao-ix.ru/announce
udp://explodie.org:6969/announce
wss://tracker.btorrent.xyz
wss://tracker.openwebtorrent.com`

var BTsets *BTSets

func SetBTSets(sets *BTSets) {
	if ReadOnly {
		return
	}
	// failsafe checks (use defaults)
	if sets.CacheSize == 0 {
		sets.CacheSize = 64 * 1024 * 1024
	}
	if sets.ConnectionsLimit == 0 {
		sets.ConnectionsLimit = 25
	}
	if sets.TorrentDisconnectTimeout == 0 {
		sets.TorrentDisconnectTimeout = 30
	}

	if sets.ReaderReadAheadMB < MinReadAheadMB {
		sets.ReaderReadAheadMB = MinReadAheadMB
	}
	if sets.ReaderReadAheadMB > MaxReadAheadMB {
		sets.ReaderReadAheadMB = MaxReadAheadMB
	}
	// Same clamp for the pause window: it can be much larger than the playing
	// one, but not past what a single reader may pin of the cache.
	if sets.PausePreloadMB > MaxReadAheadMB {
		sets.PausePreloadMB = MaxReadAheadMB
	}
	if sets.PausePreloadMB > 0 && sets.PausePreloadMB < MinReadAheadMB {
		sets.PausePreloadMB = MinReadAheadMB
	}
	if sets.PausePreloadAfterSec < 0 {
		sets.PausePreloadAfterSec = 0
	}

	if sets.PreloadCache < 0 {
		sets.PreloadCache = 0
	}
	if sets.PreloadCache > 100 {
		sets.PreloadCache = 100
	}
	// Round to hundredths for consistency
	sets.PreloadCache = math.Round(sets.PreloadCache*100) / 100

	// Absolute preload in MB: manual override, 0 = use percent above
	if sets.PreloadCacheMB < 0 {
		sets.PreloadCacheMB = 0
	}

	// Validate CacheHysteresis
	if sets.CacheHysteresis < 0.50 {
		sets.CacheHysteresis = 0.90 // Reset to default
	}

	// Clamp here too, not only in CacheStateTTLDuration: the value is written
	// back to the DB immediately below, so an out-of-range one would persist
	// and outlive this run.
	if sets.CacheStateTTL > CacheStateTTLMaxSec {
		sets.CacheStateTTL = CacheStateTTLMaxSec
	}

	if sets.TorrentsSavePath == "" {
		sets.UseDisk = false
	} else if sets.UseDisk {
		BTsets = sets

		go filepath.WalkDir(sets.TorrentsSavePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.ToLower(d.Name()) == ".tsc" {
				BTsets.TorrentsSavePath = path
				log.TLogln("Find directory \"" + BTsets.TorrentsSavePath + "\", use as cache dir")
				return io.EOF
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		})
	}

	BTsets = sets
	buf, err := json.Marshal(BTsets)
	if err != nil {
		log.TLogln("Error marshal btsets", err)
		return
	}
	tdb.Set("Settings", "BitTorr", buf)
}

func SetDefaultConfig() {
	sets := new(BTSets)
	sets.CacheSize = 64 * 1024 * 1024 // 64 MB
	sets.PreloadCache = 50.0          // 50% default
	sets.OneCacheForAll = false       // Per-torrent by default (backward compatible)
	sets.CacheHysteresis = 0.90       // Clean to 90% of capacity
	sets.CacheStateTTL = DefaultCacheStateTTLSec
	sets.ConnectionsLimit = 25
	sets.RetrackersMode = 1
	sets.TrackersListURL = DefaultTrackersListURL
	sets.DefaultTrackers = DefaultTrackersText
	sets.TorrentDisconnectTimeout = 30
	sets.ReaderReadAheadMB = DefaultReadAheadMB
	sets.ViewerIdleGraceSec = ViewerIdleGraceDefault
	// A paused viewer is asking for buffer; honour that by default rather than
	// letting it rediscover the same 90 MB every time it pauses.
	sets.PausePreloadMB = PausePreloadDefaultMB
	sets.PausePreloadAfterSec = PausePreloadAfterDefaultSec
	sets.ResponsiveMode = true
	sets.ShowFSActiveTorr = true
	sets.StoreSettingsInJson = true
	sets.EnableLPD = true
	sets.LPDIPv6 = false
	sets.EnableBonjour = true
	// Set default TMDB settings
	sets.TMDBSettings = TMDBConfig{
		APIKey:     "",
		APIURL:     "https://api.themoviedb.org",
		ImageURL:   "https://image.tmdb.org",
		ImageURLRu: "https://imagetmdb.com",
	}
	BTsets = sets
	if !ReadOnly {
		buf, err := json.Marshal(BTsets)
		if err != nil {
			log.TLogln("Error marshal btsets", err)
			return
		}
		tdb.Set("Settings", "BitTorr", buf)
	}
}

func loadBTSets() {
	buf := tdb.Get("Settings", "BitTorr")
	if len(buf) > 0 {
		err := json.Unmarshal(buf, &BTsets)
		if err == nil {
			if BTsets.ReaderReadAheadMB < MinReadAheadMB {
				BTsets.ReaderReadAheadMB = MinReadAheadMB
			}
			if BTsets.ReaderReadAheadMB > MaxReadAheadMB {
				BTsets.ReaderReadAheadMB = MaxReadAheadMB
			}
			if BTsets.ViewerIdleGraceSec < ViewerIdleGraceMin {
				BTsets.ViewerIdleGraceSec = ViewerIdleGraceDefault
			}
			// Same clamp as SetBTSets. Configs written before the field existed
			// deserialise to 0, which CacheStateTTLDuration() reads as the
			// default — only genuinely oversized values need fixing here.
			if BTsets.CacheStateTTL > CacheStateTTLMaxSec {
				BTsets.CacheStateTTL = CacheStateTTLMaxSec
			}
			// Set default TMDB settings if missing (for existing configs)
			if BTsets.TMDBSettings.APIURL == "" {
				BTsets.TMDBSettings = TMDBConfig{
					APIKey:     "",
					APIURL:     "https://api.themoviedb.org",
					ImageURL:   "https://image.tmdb.org",
					ImageURLRu: "https://imagetmdb.com",
				}
			}
			// Default Bonjour on for configs that predate the setting.
			var raw map[string]json.RawMessage
			if json.Unmarshal(buf, &raw) == nil {
				if _, ok := raw["EnableBonjour"]; !ok {
					BTsets.EnableBonjour = true
				}
			}
			// Upgrade older configs that never had tracker list fields.
			if BTsets.TrackersListURL == "" && BTsets.DefaultTrackers == "" {
				BTsets.TrackersListURL = DefaultTrackersListURL
				BTsets.DefaultTrackers = DefaultTrackersText
			}
			return
		}
		log.TLogln("Error unmarshal btsets", err)
	}
	// initialize defaults on error
	SetDefaultConfig()
}
