package metainfo

import (
	"github.com/anacrolix/torrent/metainfo"
	"io"
	"strings"
)

// GetTorrentHashHexString Returns the torrent hash for the given reader
func GetTorrentHashHexString(reader io.Reader) (string, error) {
	info, err := metainfo.Load(reader)
	if err != nil {
		return "", err
	}

	return strings.ToUpper(info.Info.Hash.HexString()), nil
}
