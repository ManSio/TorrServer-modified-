package torr

import (
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/anacrolix/torrent"

	"server/settings"
)

// retryClient policy: the port may be busy while the previous client's uTP
// socket is released lazily, so creation is retried.
func TestRetryClientSucceedsFirstTry(t *testing.T) {
	calls := 0
	_, err := retryClient(30, time.Millisecond, func() (*torrent.Client, error) {
		calls++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 attempt, got %d", calls)
	}
}

func TestRetryClientRecoversAfterBusyPort(t *testing.T) {
	calls := 0
	busy := errors.New("listen udp4 :51413: bind: address already in use")
	_, err := retryClient(30, time.Millisecond, func() (*torrent.Client, error) {
		calls++
		if calls < 12 {
			return nil, busy
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("want nil after recovery, got %v", err)
	}
	if calls != 12 {
		t.Fatalf("want 12 attempts, got %d", calls)
	}
}

func TestRetryClientGivesUpAndReports(t *testing.T) {
	calls := 0
	want := errors.New("address already in use")
	_, err := retryClient(5, time.Millisecond, func() (*torrent.Client, error) {
		calls++
		return nil, want
	})
	if err == nil {
		t.Fatal("want the last error after the attempts are spent")
	}
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
	if calls != 5 {
		t.Fatalf("want 5 attempts, got %d", calls)
	}
}

func TestRetryClientNoSleepAfterLastAttempt(t *testing.T) {
	start := time.Now()
	_, _ = retryClient(3, 40*time.Millisecond, func() (*torrent.Client, error) {
		return nil, errors.New("busy")
	})
	// Two pauses are expected; a pause after the last attempt would add a third.
	if elapsed := time.Since(start); elapsed >= 3*40*time.Millisecond {
		t.Fatalf("slept after the last attempt: %v", elapsed)
	}
}

// A second Connect without Disconnect (two settings saves racing) must not
// overwrite a live client and abandon its storage.
func TestConnectTwiceKeepsOneStorage(t *testing.T) {
	lifecycleSettings(t)
	bt := NewBTS()
	// Close the storage before the settings cleanup swaps the global (KI-017).
	t.Cleanup(bt.Disconnect)

	if err := bt.Connect(); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	runtime.GC()
	afterFirst := runtime.NumGoroutine()

	if err := bt.Connect(); err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if bt.storage == nil || bt.client == nil || bt.torrents == nil {
		t.Fatal("second Connect left a half-initialised server")
	}
	// Without closing the previous client and storage, its goroutines and its
	// cache scan would stay alive: the count would grow instead of settling.
	if got := waitGoroutines(t, afterFirst, 2); got > afterFirst+2 {
		t.Fatalf("goroutines %d -> %d: the previous client or storage was abandoned", afterFirst, got)
	}
}

// After Disconnect a fresh Connect must leave a usable server object.
func TestReconnectAfterDisconnect(t *testing.T) {
	lifecycleSettings(t)
	bt := NewBTS()
	t.Cleanup(bt.Disconnect)

	if err := bt.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	bt.Disconnect()
	if err := bt.Connect(); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if bt.client == nil || bt.storage == nil || bt.torrents == nil {
		t.Fatal("reconnect left a half-initialised server")
	}
	if bts != bt {
		t.Fatal("InitApiHelper did not point at this server")
	}
}

// --- lifecycle: a settings save must not leak the storage (KI-020) ---

func lifecycleSettings(t *testing.T) {
	t.Helper()
	prev, prevArgs := settings.BTsets, settings.Args
	t.Cleanup(func() { settings.BTsets, settings.Args = prev, prevArgs })
	settings.Args = &settings.ExecArgs{}
	settings.BTsets = &settings.BTSets{
		TorrentsSavePath: t.TempDir(),
		CacheHysteresis:  0.9,
		ConnectionsLimit: 25,
		PeersListenPort:  0,
		DisableDHT:       true,
		DisableTCP:       true,
		DisablePEX:       true,
		DisableUPNP:      true,
		DisableUpload:    true,
	}
	prevAttempts, prevPause := connectAttempts, connectPause
	t.Cleanup(func() { connectAttempts, connectPause = prevAttempts, prevPause })
	connectAttempts, connectPause = 2, 10*time.Millisecond
}

func waitGoroutines(t *testing.T, want int, tolerance int) int {
	t.Helper()
	var got int
	for i := 0; i < 40; i++ {
		runtime.GC()
		got = runtime.NumGoroutine()
		if got <= want+tolerance {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	return got
}

func TestDisconnectClosesStorage(t *testing.T) {
	lifecycleSettings(t)
	bt := NewBTS()
	t.Cleanup(bt.Disconnect)

	if err := bt.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if bt.storage == nil {
		t.Fatal("Connect left no storage")
	}
	bt.Disconnect()

	if bt.storage != nil {
		t.Fatal("Disconnect kept the storage")
	}
	if bt.client != nil {
		t.Fatal("Disconnect kept the client")
	}
}

// A failed Connect must not leave the storage (and its cache-scan goroutine)
// behind: with 60 attempts one settings save would strand 60 of them.
// Readers must not wait for the connect retry: the sleep happens under
// connectMu, not bt.mu, so /stat, /torrents and streams keep answering while
// the old uTP socket is being released (KI-020).
func TestReadersNotBlockedWhileConnectRetries(t *testing.T) {
	lifecycleSettings(t)
	connectAttempts, connectPause = 8, 100*time.Millisecond // ~0.8 s of retries

	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = port
	cfg.NoDHT = true
	cfg.DisableTCP = true
	blocker, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("blocker client: %v", err)
	}
	t.Cleanup(blocker.Close)
	_ = l.Close()

	settings.BTsets.PeersListenPort = port
	bt := NewBTS()
	done := make(chan struct{})
	go func() {
		_ = bt.Connect() // fails after the retries, but must not hold bt.mu
		close(done)
	}()
	time.Sleep(150 * time.Millisecond) // inside the retry loop by now

	start := time.Now()
	_ = bt.ListTorrents()
	_ = bt.GetTorrent([20]byte{})
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("readers waited %v for the connect retry", elapsed)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Log("connect retry still running; the reader check above is what matters")
	}
}

func TestFailedConnectDoesNotLeakStorage(t *testing.T) {
	lifecycleSettings(t)

	// Occupy the port the next Connect will ask for.
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = port
	cfg.NoDHT = true
	cfg.DisableTCP = true
	blocker, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("blocker client: %v", err)
	}
	t.Cleanup(blocker.Close)
	_ = l.Close()

	settings.BTsets.PeersListenPort = port
	bt := NewBTS()
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for i := 0; i < 3; i++ {
		if err := bt.Connect(); err == nil {
			t.Fatalf("attempt %d: Connect succeeded although the port is taken", i+1)
		}
		if bt.storage != nil {
			t.Fatalf("attempt %d: failed Connect left a storage behind", i+1)
		}
	}

	if got := waitGoroutines(t, baseline, 2); got > baseline+2 {
		t.Fatalf("goroutines grew from %d to %d after three failed Connects", baseline, got)
	}
}
