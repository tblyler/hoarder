package queue

import (
	"bytes"
	"errors"
	"github.com/fsnotify/fsnotify"
	"github.com/tblyler/easysftp"
	"github.com/tblyler/go-rtorrent/rtorrent"
	"github.com/tblyler/hoarder/metainfo"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config defines the settings for watching, uploading, and downloading
type Config struct {
	Rtorrent struct {
		Addr         string `json:"addr" yaml:"addr"`
		InsecureCert bool   `json:"insecure_cert" yaml:"insecure_cert"`
		Username     string `json:"username" yaml:"username"`
		Password     string `json:"password" yaml:"password"`
	} `json:"rtorrent" yaml:"rtorrent,flow"`

	SSH struct {
		Username string        `json:"username" yaml:"username"`
		Password string        `json:"password" yaml:"password"`
		KeyPath  string        `json:"privkey_path" yaml:"privkey_path"`
		Addr     string        `json:"addr" yaml:"addr"`
		Timeout  time.Duration `json:"connect_timeout" yaml:"connect_timeout"`
	} `json:"ssh" yaml:"ssh,flow"`

	DownloadFileMode          os.FileMode       `json:"file_download_filemode" yaml:"file_download_filemode"`
	WatchDownloadPaths        map[string]string `json:"watch_to_download_paths" yaml:"watch_to_download_paths,flow"`
	TempDownloadPath          string            `json:"temp_download_path" yaml:"temp_download_path"`
	FinishedTorrentFilePath   map[string]string `json:"watch_to_finish_path" yaml:"watch_to_finish_path,flow"`
	TorrentListUpdateInterval time.Duration     `json:"rtorrent_update_interval" yaml:"rtorrent_update_interval"`
	ConcurrentDownloads       uint              `json:"download_jobs" yaml:"download_jobs"`
	ResumeDownloads           bool              `json:"resume_downloads" yaml:"resume_downloads"`
}

// Queue watches the given folders for new .torrent files,
// uploads them to the given rTorrent server,
// and then downloads them over SSH to the given download path.
type Queue struct {
	rtClient      *rtorrent.RTorrent
	sftpClient    *easysftp.Client
	fsWatcher     *fsnotify.Watcher
	config        *Config
	torrentList   map[string]rtorrent.Torrent
	downloadQueue map[string]string
	logger        *log.Logger
	lock          sync.RWMutex
}

var prettyBytesValues = []float64{
	1024,
	1024 * 1024,
	1024 * 1024 * 1024,
	1024 * 1024 * 1024 * 1024,
	1024 * 1024 * 1024 * 1024 * 1024,
	1024 * 1024 * 1024 * 1024 * 1024 * 1024,
}

var prettyBytesNames = []string{
	"KiB",
	"MiB",
	"GiB",
	"TiB",
	"PiB",
	"EiB",
}

func prettyBytes(bytes float64) string {
	output := strconv.FormatFloat(bytes, 'f', 2, 64) + "B"
	for i, divisor := range prettyBytesValues {
		newBytes := bytes / divisor
		if newBytes > 1024 {
			continue
		}

		if newBytes < 1 {
			break
		}

		output = strconv.FormatFloat(newBytes, 'f', 2, 64) + prettyBytesNames[i]
	}

	return output
}

// NewQueue establishes all connections and watchers
func NewQueue(config *Config, logger *log.Logger) (*Queue, error) {
	if config.WatchDownloadPaths == nil || len(config.WatchDownloadPaths) == 0 {
		return nil, errors.New("Must have queue.QueueConfig.WatchDownloadPaths set")
	}

	for watchPath, downloadPath := range config.WatchDownloadPaths {
		config.WatchDownloadPaths[filepath.Clean(watchPath)] = filepath.Clean(downloadPath)
	}

	if config.ConcurrentDownloads == 0 {
		config.ConcurrentDownloads = 1
	}

	if logger == nil {
		logger = log.New(ioutil.Discard, "", 0)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for watchPath := range config.WatchDownloadPaths {
		err = fsWatcher.Add(watchPath)
		if err != nil {
			return nil, err
		}
	}

	rtClient := rtorrent.New(config.Rtorrent.Addr, config.Rtorrent.InsecureCert)
	rtClient.SetAuth(config.Rtorrent.Username, config.Rtorrent.Password)

	sftpClient, err := easysftp.Connect(&easysftp.ClientConfig{
		Username: config.SSH.Username,
		Password: config.SSH.Password,
		KeyPath:  config.SSH.KeyPath,
		Host:     config.SSH.Addr,
		Timeout:  config.SSH.Timeout,
		FileMode: config.DownloadFileMode,
	})
	if err != nil {
		return nil, err
	}

	return &Queue{
		rtClient:      rtClient,
		sftpClient:    sftpClient,
		fsWatcher:     fsWatcher,
		config:        config,
		downloadQueue: make(map[string]string),
		logger:        logger,
	}, nil
}

// Close all of the connections and watchers
func (q *Queue) Close() []error {
	errs := []error{}
	err := q.sftpClient.Close()
	if err != nil {
		errs = append(errs, err)
	}

	err = q.fsWatcher.Close()
	if err != nil {
		errs = append(errs, err)
	}

	return errs
}

func (q *Queue) updateTorrentList() error {
	torrents, err := q.rtClient.GetTorrents(rtorrent.ViewMain)
	if err != nil {
		return err
	}

	torrentList := make(map[string]rtorrent.Torrent)

	for _, torrent := range torrents {
		torrentList[torrent.Hash] = torrent
	}

	q.torrentList = torrentList

	return nil
}

func (q *Queue) addTorrentFilePath(path string) error {
	// lock for downloadQueue
	q.lock.Lock()
	defer q.lock.Unlock()

	torrentData, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	torrentHash, err := metainfo.GetTorrentHashHexString(bytes.NewReader(torrentData))
	if err != nil {
		return err
	}

	q.downloadQueue[torrentHash] = path

	return q.rtClient.AddTorrent(torrentData)
}

func (q *Queue) getFinishedTorrents() []rtorrent.Torrent {
	q.lock.RLock()
	torrents := []rtorrent.Torrent{}
	for hash, torrentPath := range q.downloadQueue {
		torrent, exists := q.torrentList[hash]
		if !exists {
			q.lock.RUnlock()
			err := q.addTorrentFilePath(torrentPath)
			q.lock.RLock()
			if err != nil {
				q.logger.Printf("Unable to add torrent '%s' error '%s'", torrentPath, err)
			}

			continue
		}

		if !torrent.Completed {
			continue
		}

		torrents = append(torrents, torrent)
	}

	return torrents
}

func (q *Queue) downloadTorrents(torrents []rtorrent.Torrent) {
	q.lock.RLock()
	defer q.lock.RUnlock()

	// this will keep track of the torrent hashes that have finished
	// will return an empty string on a failed download
	done := make(chan string, q.config.ConcurrentDownloads)
	var running uint
	for _, torrent := range torrents {
		if !torrent.Completed {
			continue
		}

		torrent.Hash = strings.ToUpper(torrent.Hash)

		torrentFilePath, exists := q.downloadQueue[torrent.Hash]
		if !exists {
			continue
		}

		if running >= q.config.ConcurrentDownloads {
			finishedHash := <-done
			if finishedHash != "" {
				q.lock.RUnlock()
				q.lock.Lock()
				delete(q.downloadQueue, finishedHash)
				q.lock.Unlock()
				q.lock.RLock()
			}

			running--
		}

		var downloadPath string
		destDownloadPath := q.config.WatchDownloadPaths[filepath.Dir(torrentFilePath)]

		if q.config.TempDownloadPath == "" {
			downloadPath = destDownloadPath
		} else {
			downloadPath = filepath.Join(q.config.TempDownloadPath, destDownloadPath)

			if info, err := os.Stat(downloadPath); os.IsExist(err) {
				if !info.IsDir() {
					q.logger.Printf("Unable to downlaod to temp path '%s' since it is not a directory", downloadPath)
					continue
				}
			}

			err := os.MkdirAll(downloadPath, q.config.DownloadFileMode)
			if err != nil {
				q.logger.Printf("Unable to create temp download path '%s' error '%s'", downloadPath, err)
				continue
			}
		}

		go func(torrentFilePath string, downloadPath string, torrent rtorrent.Torrent) {
			q.logger.Printf("Downloading '%s' (%s) to '%s' (%s) %s", torrent.Name, torrentFilePath, downloadPath, destDownloadPath, prettyBytes(float64(torrent.Size)))
			err := q.sftpClient.Mirror(torrent.Path, downloadPath, q.config.ResumeDownloads)
			if err != nil {
				q.logger.Printf("Failed to download '%s' to '%s' error '%s'", torrent.Path, downloadPath, err)
				done <- ""
				return
			}

			// we need to move the downlaod from the temp directory to the destination
			if downloadPath != destDownloadPath {
				fileName := filepath.Base(torrent.Path)
				downFullPath := filepath.Join(downloadPath, fileName)
				destFullPath := filepath.Join(destDownloadPath, fileName)
				err = os.Rename(downFullPath, destFullPath)
				if err != nil {
					q.logger.Printf("Failed to move temp path '%s' to dest path '%s' error '%s'", downFullPath, destFullPath, err)
					done <- ""
					return
				}
			}

			parentTorrentPath := filepath.Dir(torrentFilePath)
			if movePath, exists := q.config.FinishedTorrentFilePath[parentTorrentPath]; exists {
				destFullPath := filepath.Join(movePath, filepath.Base(torrentFilePath))
				err = os.Rename(torrentFilePath, destFullPath)
				if err != nil {
					q.logger.Printf("Failed to move torrent file from '%s' to '%s' error '%s'", torrentFilePath, destFullPath, err)
				}
			} else {
				err = os.Remove(torrentFilePath)
				if err != nil {
					q.logger.Printf("Failed to remove torrent file '%s' error '%s'", torrentFilePath, err)
				}
			}

			q.logger.Printf("Successfully downloaded '%s' (%s)", torrent.Name, torrentFilePath)

			done <- torrent.Hash
		}(torrentFilePath, downloadPath, torrent)
		running++
	}

	for running > 0 {
		finishedHash := <-done
		if finishedHash != "" {
			q.lock.RUnlock()
			q.lock.Lock()
			delete(q.downloadQueue, finishedHash)
			q.lock.Unlock()
			q.lock.RLock()
		}

		running--
	}
}

// Run executes all steps needed for looking at the queue, catching updates, and processing all work
func (q *Queue) Run(stop <-chan bool) {
	// for the initial run, load all files in the specified directories
	for localPath := range q.config.WatchDownloadPaths {
		files, err := ioutil.ReadDir(localPath)
		if err != nil {
			q.logger.Printf("Unable to read local path '%s' error '%s'", localPath, err)
		}

		for _, file := range files {
			// skip directories or files that do not end with .torrent
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".torrent") {
				continue
			}

			fullPath := filepath.Join(localPath, file.Name())

			q.logger.Printf("Adding %s to download queue", fullPath)
			err = q.addTorrentFilePath(fullPath)
			if err != nil {
				q.logger.Printf("Failed to add torrent at '%s' error '%s'", fullPath, err)
			}
		}
	}

	go func() {
		lastUpdateTime := time.Time{}
		for {
			if time.Now().Sub(lastUpdateTime) >= q.config.TorrentListUpdateInterval {
				err := q.updateTorrentList()
				if err == nil {
					lastUpdateTime = time.Now()
				} else {
					q.logger.Printf("Failed to update torrent list from rTorrent: '%s'", err)
				}
			}

			downloadTorrents := q.getFinishedTorrents()
			if len(downloadTorrents) > 0 {
				q.downloadTorrents(downloadTorrents)
			}

			time.Sleep(time.Second)
		}
	}()

	// watch all directories for file changes
	var err error
	finished := false
	for !finished {
		select {
		case event := <-q.fsWatcher.Events:
			if event.Op&fsnotify.Write == fsnotify.Write ||
				event.Op&fsnotify.Create == fsnotify.Create ||
				event.Op&fsnotify.Rename == fsnotify.Rename {
				// skip files that do not end with .torrent
				if !strings.HasSuffix(event.Name, ".torrent") {
					break
				}

				q.logger.Printf("Adding %s to download queue", event.Name)
				err = q.addTorrentFilePath(event.Name)
				if err != nil {
					q.logger.Printf("Failed to add '%s' error '%s'", event.Name, err)
				}
			}

		case err = <-q.fsWatcher.Errors:
			q.logger.Printf("Error while watching folders '%s'", err)

		case <-stop:
			finished = true
		}
	}
}
