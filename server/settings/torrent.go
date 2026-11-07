package settings

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

type TorrentDB struct {
	*torrent.TorrentSpec

	Title    string `json:"title,omitempty"`
	Category string `json:"category,omitempty"`
	Poster   string `json:"poster,omitempty"`
	Data     string `json:"data,omitempty"`

	Timestamp int64 `json:"timestamp,omitempty"`
	Size      int64 `json:"size,omitempty"`
}

type File struct {
	Name string `json:"name,omitempty"`
	Id   int    `json:"id,omitempty"`
	Size int64  `json:"size,omitempty"`
}

var mu sync.Mutex

// ListTorrent is called by every /torrents list and by the web UI polls. Each
// call used to decode every stored torrent, including its metadata, which
// showed up as a multi-megabyte allocation in the heap profile. The result is
// cached for a short TTL and invalidated by AddTorrent/RemTorrent, so a poll
// costs a slice copy instead of a full DB decode.
const listTorrentCacheTTL = time.Second

var listTorrentCache struct {
	sync.Mutex
	list []*TorrentDB
	at   time.Time
}

func invalidateListTorrentCache() {
	listTorrentCache.Lock()
	listTorrentCache.list = nil
	listTorrentCache.at = time.Time{}
	listTorrentCache.Unlock()
}

func cloneTorrentList(list []*TorrentDB) []*TorrentDB {
	if list == nil {
		return nil
	}
	return append([]*TorrentDB(nil), list...)
}

func AddTorrent(torr *TorrentDB) {
	list := ListTorrent()
	mu.Lock()
	find := -1
	for i, db := range list {
		if db.InfoHash.HexString() == torr.InfoHash.HexString() {
			find = i
			break
		}
	}
	if find != -1 {
		list[find] = torr
	} else {
		list = append(list, torr)
	}
	for _, db := range list {
		buf, err := json.Marshal(db)
		if err == nil {
			tdb.Set("Torrents", db.InfoHash.HexString(), buf)
		}
	}
	mu.Unlock()
	invalidateListTorrentCache()
}

func ListTorrent() []*TorrentDB {
	listTorrentCache.Lock()
	if listTorrentCache.list != nil && time.Since(listTorrentCache.at) < listTorrentCacheTTL {
		cached := cloneTorrentList(listTorrentCache.list)
		listTorrentCache.Unlock()
		return cached
	}
	listTorrentCache.Unlock()

	// Use read lock to prevent migration during read
	dbMigrationLock.RLock()
	defer dbMigrationLock.RUnlock()

	mu.Lock()
	defer mu.Unlock()

	var list []*TorrentDB
	keys := tdb.List("Torrents")
	for _, key := range keys {
		buf := tdb.Get("Torrents", key)
		if len(buf) > 0 {
			var torr *TorrentDB
			err := json.Unmarshal(buf, &torr)
			if err == nil {
				list = append(list, torr)
			}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Timestamp > list[j].Timestamp
	})
	listTorrentCache.Lock()
	listTorrentCache.list = list
	listTorrentCache.at = time.Now()
	listTorrentCache.Unlock()
	return cloneTorrentList(list)
}

func RemTorrent(hash metainfo.Hash) {
	mu.Lock()
	tdb.Rem("Torrents", hash.HexString())
	mu.Unlock()
	invalidateListTorrentCache()
}
