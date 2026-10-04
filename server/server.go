package server

import (
	"os"
	"strconv"
	"strings"

	"server/tgbot"

	"server/log"
	"server/netbind"
	"server/settings"
	"server/torr/utils"
	"server/web"
)

func Start() {
	settings.InitSets(settings.Args.RDB, settings.Args.SearchWA)
	// https checks
	if settings.Args.Ssl {
		// set settings ssl enabled
		settings.Ssl = settings.Args.Ssl
		if settings.Args.SslPort == "" {
			dbSSlPort := strconv.Itoa(settings.BTsets.SslPort)
			if dbSSlPort != "0" {
				settings.Args.SslPort = dbSSlPort
			} else {
				settings.Args.SslPort = "8091"
			}
		} else { // store ssl port from params to DB
			dbSSlPort, err := strconv.Atoi(settings.Args.SslPort)
			if err == nil {
				settings.BTsets.SslPort = dbSSlPort
			}
		}
		// check if ssl cert and key files exist
		if settings.Args.SslCert != "" && settings.Args.SslKey != "" {
			// set settings ssl cert and key files
			settings.BTsets.SslCert = settings.Args.SslCert
			settings.BTsets.SslKey = settings.Args.SslKey
		}
		log.TLogln("Check web ssl port", settings.Args.SslPort)
		if err := netbind.CheckPort(settings.Args.IPs, settings.Args.SslPort); err != nil {
			log.TLogln("Port", settings.Args.SslPort, "already in use! Please set different ssl port for HTTPS. Abort")
			os.Exit(1)
		}
	}
	// http checks
	if settings.Args.Port == "" {
		settings.Args.Port = "8090"
	}

	log.TLogln("Check web port", settings.Args.Port, "on", netbind.Normalize(settings.Args.IPs))
	if err := netbind.CheckPort(settings.Args.IPs, settings.Args.Port); err != nil {
		log.TLogln("Cannot bind HTTP port", settings.Args.Port+":", err)
		os.Exit(1)
	}
	// Old disk caches are no longer cleaned here: torrstor.Storage runs a
	// reconciler that measures the cache directory, enforces the limit and
	// reclaims cold data (including at startup). cleanCache() duplicated that
	// with its own budget and raced it — on 2026-10-01 both walked the same
	// tree at once, and cleanCache deleted the 28 GB cache of a torrent that
	// was merely not in the list, outside any limit check.
	// set settings http and https ports. Start web server.
	settings.Port = settings.Args.Port
	settings.SslPort = settings.Args.SslPort
	settings.IPs = settings.Args.IPs

	if settings.Args.TGToken != "" {
		if err := tgbot.Start(settings.Args.TGToken); err != nil {
			log.TLogln("tg bot start failed", err)
		}
	}
	web.Start()
}

func WaitServer() string {
	err := web.Wait()
	if err != nil {
		return err.Error()
	}
	return ""
}

func Stop() {
	web.Stop()
	settings.CloseDB()
}

func AddTrackers(trackers string) {
	lines := strings.Split(trackers, "\n")
	var tracks []string
	for _, l := range lines {
		l = strings.Trim(l, " ,\r")
		if l != "" {
			tracks = append(tracks, l)
		}
	}
	utils.SetDefTrackers(tracks)
}
