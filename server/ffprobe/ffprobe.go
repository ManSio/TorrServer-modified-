package ffprobe

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/vansante/go-ffprobe.v2"
)

var binFile = "ffprobe"

func init() {
	path, err := exec.LookPath("ffprobe")
	if err == nil {
		ffprobe.SetFFProbeBinPath(path)
		binFile = path
	} else {
		// working dir
		if _, err := os.Stat("ffprobe"); os.IsNotExist(err) {
			ffprobe.SetFFProbeBinPath(filepath.Dir(os.Args[0]) + "/ffprobe")
			binFile = filepath.Dir(os.Args[0]) + "/ffprobe"
		}
	}
}

func Exists() bool {
	_, err := os.Stat(binFile)
	return !os.IsNotExist(err)
}

func probeContext() (context.Context, context.CancelFunc) {
	// WithTimeout cancels itself when it fires and is released by defer cancel,
	// so a probe that returns early no longer leaves a goroutine sleeping for
	// five minutes.
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

func ProbeUrl(link string) (*ffprobe.ProbeData, error) {
	ctx, cancel := probeContext()
	defer cancel()
	return ffprobe.ProbeURL(ctx, link)
}

func ProbeReader(reader io.Reader) (*ffprobe.ProbeData, error) {
	ctx, cancel := probeContext()
	defer cancel()
	return ffprobe.ProbeReader(ctx, reader)
}
