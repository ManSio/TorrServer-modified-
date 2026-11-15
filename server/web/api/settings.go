package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"server/bonjour"
	"server/dlna"
	"server/rutor"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	sets "server/settings"
	"server/torr"
)

// Action: get, set, def
type setsReqJS struct {
	requestI
	Sets *sets.BTSets `json:"sets,omitempty"`
}

// settings godoc
//
//	@Summary		Get / Set server settings
//	@Description	Allow to get or set server settings.
//
//	@Tags			API
//
//	@Param			request	body	setsReqJS	true	"Settings request. Available params for action: get, set, def"
//
//	@Accept			json
//	@Produce		json
//	@Security		BasicAuth
//	@Success		200	{object}	sets.BTSets	"Settings JSON or nothing. Depends on what action has been asked."
//	@Router			/settings [post]
func settings(c *gin.Context) {
	var req setsReqJS
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.AbortWithError(http.StatusBadRequest, err)
		return
	}

	if req.Action == "get" {
		c.Data(200, "application/json; charset=utf-8", settingsJSONBytes())
		return
	} else if req.Action == "set" {
		if req.Sets == nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "sets is empty"})
			return
		}
		torr.SetSettings(req.Sets)
		invalidateSettingsJSON()
		restartServices()
		c.Status(200)
		return
	} else if req.Action == "def" {
		torr.SetDefSettings()
		invalidateSettingsJSON()
		restartServices()
		c.Status(200)
		return
	}
	c.AbortWithError(http.StatusBadRequest, errors.New("action is empty"))
}

// restartServices applies the settings now in effect to DLNA, Bonjour and the
// rutor search. Reading sets.BTsets (not the request) also keeps read-only mode
// consistent: the request is not applied there, so the services must follow what
// is actually in use.
var restartServicesMu sync.Mutex

func restartServices() {
	// Serialize concurrent /settings requests: interleaved Stop/Start pairs can
	// otherwise leave a service stopped although the final settings enable it.
	restartServicesMu.Lock()
	defer restartServicesMu.Unlock()

	dlna.Stop()
	if sets.BTsets.EnableDLNA {
		dlna.Start()
	}
	bonjour.Stop()
	if sets.BTsets.EnableBonjour {
		bonjour.Start()
	}
	rutor.Stop()
	rutor.Start()
}

// The web client polls /settings with action=get constantly - 9437 of the last
// 20000 requests on the live server were this endpoint. Every one of them
// re-marshalled the whole configuration into JSON to hand back bytes that were
// identical to the previous 9437.
//
// The marshalled form is cached and rebuilt only when something actually
// changes the settings, so a poll costs a slice lookup instead of a full
// encode of a struct with dozens of fields and long tracker lists.
var settingsJSONCache struct {
	mu    sync.Mutex
	data  []byte
	valid bool
}

func settingsJSONBytes() []byte {
	settingsJSONCache.mu.Lock()
	defer settingsJSONCache.mu.Unlock()

	if settingsJSONCache.valid && settingsJSONCache.data != nil {
		return settingsJSONCache.data
	}
	buf, err := json.Marshal(sets.BTsets)
	if err != nil {
		// Fall back to encoding per request rather than caching a failure or
		// an empty body: the client would keep the bad answer for good.
		buf, _ = json.Marshal(nil)
		return buf
	}
	settingsJSONCache.data = buf
	settingsJSONCache.valid = true
	return buf
}

func invalidateSettingsJSON() {
	settingsJSONCache.mu.Lock()
	settingsJSONCache.valid = false
	settingsJSONCache.data = nil
	settingsJSONCache.mu.Unlock()
}
