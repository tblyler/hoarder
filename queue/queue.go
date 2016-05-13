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
	"strings"
	"sync"
	"time"
)

// Config defines the settings for watching, uploading, and downloading
type Config struct {
	RtorrentAddr              string            `json:"rtorrent_addr"`
	RtorrentInsecureCert      bool              `json:"rtorrent_insecure_cert"`
	RtorrentUsername          string            `json:"rtorrent_username"`
	RtorrentPassword          string            `json:"rtorrent_password"`
	SSHUsername               string            `json:"ssh_username"`
	SSHPassword               string            `json:"ssh_password"`
	SSHKeyPath                string            `json:"ssh_privkey_path"`
	SSHAddr                   string            `json:"ssh_addr"`
	SSHTimeout                time.Duration     `json:"ssh_connect_timeout"`
	DownloadFileMode          os.FileMode       `json:"file_download_filemode"`
	WatchDownloadPaths        map[string]string `json:"watch_to_download_paths"`
	TempDownloadPath          string            `json:"temp_download_path"`
	FinishedTorrentFilePath   map[string]string `json:"watch_to_finish_path"`
	TorrentListUpdateInterval time.Duration     `json:"rtorrent_update_interval"`
	ConcurrentDownloads       uint              `json:"download_jobs"`
}

// Queue watches the given folders for new .torrent files,
// uploads them to the given rTorrent server,
// and then downloads them over SSH to the given download path.
type Queue struct {
	rtClient          *rtorrent.RTorrent
	sftpClient        *easysftp.Client
	fsWatcher         *fsnotify.Watcher
	config            *Config
	torrentList       map[string]rtorrent.Torrent
	torrentListUpdate time.Time
	downloadQueue     map[string]string
	logger            *log.Logger
	lock              sync.RWMutex
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

	rtClient := rtorrent.New(config.RtorrentAddr, config.RtorrentInsecureCert)
	rtClient.SetAuth(config.RtorrentUsername, config.RtorrentPassword)

	sftpClient, err := easysftp.Connect(&easysftp.ClientConfig{
		Username: config.SSHUsername,
		Password: config.SSHPassword,
		KeyPath:  config.SSHKeyPath,
		Host:     config.SSHAddr,
		Timeout:  config.SSHTimeout,
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
	// lock for torrentList
	q.lock.Lock()
	defer q.lock.Unlock()

	torrents, err := q.rtClient.GetTorrents(rtorrent.ViewMain)
	if err != nil {
		return err
	}

	torrentList := make(map[string]rtorrent.Torrent)

	for _, torrent := range torrents {
		torrentList[torrent.Hash] = torrent
	}

	q.torrentList = torrentList
	q.torrentListUpdate = time.Now()

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
	// lock for torrentList
	q.lock.RLock()
	defer q.lock.RUnlock()

	torrents := []rtorrent.Torrent{}
	for hash, torrentPath := range q.downloadQueue {
		torrent, exists := q.torrentList[hash]
		if !exists {
			err := q.addTorrentFilePath(torrentPath)
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
	// lock for downloadQueue
	q.lock.RLock()
	defer q.lock.RUnlock()

	done := make(chan bool, q.config.ConcurrentDownloads)
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
			<-done
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
			err := q.sftpClient.Mirror(torrent.Path, downloadPath)
			if err != nil {
				q.logger.Printf("Failed to download '%s' to '%s' error '%s'", torrent.Path, downloadPath, err)
				done <- false
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
					done <- false
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

			q.lock.RUnlock()
			q.lock.Lock()
			delete(q.downloadQueue, torrent.Hash)
			q.lock.Unlock()
			q.lock.RLock()
			done <- true
		}(torrentFilePath, downloadPath, torrent)
		running++
	}

	for running > 0 {
		<-done
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
		err := q.updateTorrentList()
		if err != nil {
			q.logger.Printf("Failed to update torrent list from rTorrent: '%s'", err)
		}

		if q.config.TorrentListUpdateInterval == 0 {
			time.Sleep(time.Minute)
		} else {
			time.Sleep(q.config.TorrentListUpdateInterval)
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
