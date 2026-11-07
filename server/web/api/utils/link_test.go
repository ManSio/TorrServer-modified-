package utils

import (
	"strings"
	"testing"
)

func TestParseLinkRecognizesSchemes(t *testing.T) {
	hash := strings.Repeat("a", 40)

	spec, err := ParseLink("magnet:?xt=urn:btih:" + hash + "&dn=Test")
	if err != nil {
		t.Fatalf("magnet: %v", err)
	}
	if spec.InfoHash.HexString() != hash || spec.DisplayName != "Test" {
		t.Fatalf("magnet spec: %+v", spec)
	}

	spec, err = ParseLink(hash)
	if err != nil {
		t.Fatalf("bare hash: %v", err)
	}
	if spec.InfoHash.HexString() != hash {
		t.Fatalf("bare hash spec: %+v", spec)
	}

	if _, err := ParseLink("ftp://example.org/file"); err == nil {
		t.Fatal("unknown scheme accepted")
	}
	if _, err := ParseLink("magnet:?xt=urn:btih:zz"); err == nil {
		t.Fatal("invalid magnet accepted")
	}
}

func TestParseTorrsHashRejectsBadToken(t *testing.T) {
	if _, _, err := ParseTorrsHash("torrs://not-a-real-token"); err == nil {
		t.Fatal("bad torrs token accepted")
	}
}
