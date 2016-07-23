package queue

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"github.com/shirou/gopsutil/disk"
	"github.com/tblyler/easysftp"
	"github.com/tblyler/go-rtorrent/rtorrent"
	"github.com/tblyler/hoarder/metainfo"
	"io/ioutil"
	"log"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	RPCSocketPath             string            `json:"rpc_socket_path" yaml:"rpc_socket_path"`
	TorrentListUpdateInterval time.Duration     `json:"rtorrent_update_interval" yaml:"rtorrent_update_interval"`
	ConcurrentDownloads       uint              `json:"download_jobs" yaml:"download_jobs"`
	ResumeDownloads           bool              `json:"resume_downloads" yaml:"resume_downloads"`
	CheckDiskSpace            bool              `json:"check_disk_space" yaml:"check_disk_space"`
	MinDiskSpace              uint64            `json:"min_disk_space" yaml:"min_disk_space"`
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
	rpcSocket     net.Listener
	rpcQueue      chan RPCReq
}

type downloadInfo struct {
	path string
	size int
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

func newSftpClient(config *Config) (*easysftp.Client, error) {
	return easysftp.Connect(&easysftp.ClientConfig{
		Username: config.SSH.Username,
		Password: config.SSH.Password,
		KeyPath:  config.SSH.KeyPath,
		Host:     config.SSH.Addr,
		Timeout:  config.SSH.Timeout,
		FileMode: config.DownloadFileMode,
	})
}

// NewQueue establishes all connections and watchers
func NewQueue(config *Config, logger *log.Logger) (*Queue, error) {
	if config.WatchDownloadPaths == nil || len(config.WatchDownloadPaths) == 0 {
		return nil, errors.New("Must have queue.QueueConfig.WatchDownloadPaths set")
	}

	if len(config.RPCSocketPath) == 0 {
		return nil, errors.New("Must have queue.QueueConfig.RPCSocketPath set")
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

	sftpClient, err := newSftpClient(config)
	if err != nil {
		return nil, err
	}

	// the sftpClient connection was only made to verify settings
	sftpClient.Close()

	// Set up RPC
	rpcQueue := make(chan RPCReq)
	status := Status{rpcQueue}
	rpc.Register(&status)
	rpcSocket, err := net.Listen("unix", config.RPCSocketPath)
	if err != nil {
		return nil, err
	}
	go rpc.Accept(rpcSocket)

	q := &Queue{
		rtClient:      rtClient,
		sftpClient:    nil,
		fsWatcher:     fsWatcher,
		config:        config,
		downloadQueue: make(map[string]string),
		logger:        logger,
		rpcSocket:     rpcSocket,
		rpcQueue:      rpcQueue,
	}

	return q, nil
}

// Close all of the connections and watchers
func (q *Queue) Close() []error {
	errs := []error{}

	if q.sftpClient != nil {
		err := q.sftpClient.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}

	err := q.fsWatcher.Close()
	if err != nil {
		errs = append(errs, err)
	}

	err = q.rpcSocket.Close()
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
	torrentData, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	torrentHash, err := metainfo.GetTorrentHashHexString(bytes.NewReader(torrentData))
	if err != nil {
		return err
	}

	q.downloadQueue[torrentHash] = path

	if _, exists := q.torrentList[torrentHash]; exists {
		// the torrent is already on the server
		return nil
	}

	err = q.rtClient.AddTorrent(torrentData)
	if err != nil {
		return err
	}

	return q.updateTorrentList()
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})
	return size, err
}

/* Output looks like
Totally.Legit.Download.x264-KILLERS   |===============>              |  (50%)
ubuntu.13.37.iso                      |===>                          |  ( 7%)
Errored.Download.mkv                  |                              |  (error: could not stat file)
*/
func (q *Queue) getDownloadStatus(downloadsRunning map[string]downloadInfo) string {
	// We use maps so that we can traverse in name order
	paths := make(map[string]string, len(downloadsRunning))
	names := make([]string, 0, len(downloadsRunning))
	sizes := make(map[string]int, len(downloadsRunning))
	for _, info := range downloadsRunning {
		name := filepath.Base(info.path)
		names = append(names, name)
		paths[name] = info.path
		sizes[name] = info.size
	}
	// Sort the torrent names so that they don't jump around every time this function is called
	sort.Strings(names)

	maxNameLen := 0
	for _, name := range names {
		if len(name) > maxNameLen {
			maxNameLen = len(name)
		}
	}

	output := ""
	downloadBarLength := 30

	for nameIdx, name := range names {
		size := sizes[name]
		path := paths[name]

		// Get the size of the data that we've downloaded so far
		var bytesDownloaded int64
		stat, err := os.Stat(path)
		if err == nil {
			if stat.IsDir() {
				bytesDownloaded, err = dirSize(path)
			} else {
				bytesDownloaded = stat.Size()
			}
		}

		// Make the names and pad them with spaces on the right
		output += name
		for i := 0; i < maxNameLen-len(name); i++ {
			output += " "
		}
		output += "   |"

		// Make the download bar
		if err == nil {
			// Make the bar proportional to the amount downloaded vs the total size, and make the
			// final character a '>'
			percentDone := float64(bytesDownloaded) / float64(size)
			partialBarLength := int(float64(downloadBarLength) * percentDone)
			for i := 0; i < partialBarLength-1; i++ {
				output += "="
			}

			if partialBarLength > 0 {
				output += ">"
			}

			for i := 0; i < downloadBarLength-partialBarLength; i++ {
				output += " "
			}
			output += fmt.Sprintf("| (%2v%%)", int(100.0*percentDone))
		} else {
			for i := 0; i < downloadBarLength; i++ {
				output += " "
			}
			output += fmt.Sprintf("| (error: %s)", err)
		}

		// Add a newline if there are more downloads to show
		if nameIdx < len(names)-1 {
			output += "\n"
		}
	}
	return output
}

func (q *Queue) getFinishedTorrents() []rtorrent.Torrent {
	torrents := []rtorrent.Torrent{}
	for hash, torrentPath := range q.downloadQueue {
		torrent, exists := q.torrentList[hash]
		if !exists {
			err := q.addTorrentFilePath(torrentPath)
			if err == nil {
				q.logger.Printf("Added torrent '%s' to rTorrent", torrentPath)
			} else {
				q.logger.Printf("Unable to add torrent '%s' error '%s'", torrentPath, err)
			}

			continue
		}

		if !torrent.Completed {
			continue
		}

		torrent.Hash = strings.ToUpper(torrent.Hash)

		torrents = append(torrents, torrent)
	}

	return torrents
}

func (q *Queue) downloadTorrent(torrent rtorrent.Torrent, torrentFilePath string) error {
	if !torrent.Completed {
		return fmt.Errorf("'%s' is not a completed torrent, not downloading", torrentFilePath)
	}

	var downloadPath string
	destDownloadPath := q.config.WatchDownloadPaths[filepath.Dir(torrentFilePath)]

	if q.config.TempDownloadPath == "" {
		downloadPath = destDownloadPath
	} else {
		downloadPath = filepath.Join(q.config.TempDownloadPath, destDownloadPath)

		if info, err := os.Stat(downloadPath); os.IsExist(err) {
			if !info.IsDir() {
				return fmt.Errorf("Unable to download to temp path '%s' since it is not a directory", downloadPath)
			}
		}

		err := os.MkdirAll(downloadPath, q.config.DownloadFileMode)
		if err != nil {
			return fmt.Errorf("Unable to create temp download path '%s' error '%s'", downloadPath, err)
		}
	}

	q.logger.Printf("Downloading '%s' (%s) to '%s' (%s) %s", torrent.Name, torrentFilePath, downloadPath, destDownloadPath, prettyBytes(float64(torrent.Size)))
	err := q.sftpClient.Mirror(torrent.Path, downloadPath, q.config.ResumeDownloads)
	if err != nil {
		return fmt.Errorf("Failed to download '%s' to '%s' error '%s'", torrent.Path, downloadPath, err)
	}

	// we need to move the downlaod from the temp directory to the destination
	if downloadPath != destDownloadPath {
		fileName := filepath.Base(torrent.Path)
		downFullPath := filepath.Join(downloadPath, fileName)
		destFullPath := filepath.Join(destDownloadPath, fileName)
		err = os.Rename(downFullPath, destFullPath)
		if err != nil {
			return fmt.Errorf("Failed to move temp path '%s' to dest path '%s' error '%s'", downFullPath, destFullPath, err)
		}
	}

	parentTorrentPath := filepath.Dir(torrentFilePath)
	if movePath, exists := q.config.FinishedTorrentFilePath[parentTorrentPath]; exists {
		destFullPath := filepath.Join(movePath, filepath.Base(torrentFilePath))
		err = os.Rename(torrentFilePath, destFullPath)
		if err != nil && os.IsExist(err) {
			return fmt.Errorf("Failed to move torrent file from '%s' to '%s' error '%s'", torrentFilePath, destFullPath, err)
		}
	} else {
		err = os.Remove(torrentFilePath)
		if err != nil && os.IsExist(err) {
			return fmt.Errorf("Failed to remove torrent file '%s' error '%s'", torrentFilePath, err)
		}
	}

	q.logger.Printf("Successfully downloaded '%s' (%s)", torrent.Name, torrentFilePath)

	return nil
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
			if err == nil {
				q.logger.Printf("Added torrent '%s' to rTorrent", fullPath)
			} else {
				q.logger.Printf("Failed to add torrent at '%s' error '%s'", fullPath, err)
			}
		}
	}

	// watch all directories for file changes
	var err error
	finished := false
	lastUpdateTime := time.Time{}
	downloadedHashes := make(chan string, q.config.ConcurrentDownloads)
	downloadsRunning := make(map[string]downloadInfo)
	for {
		cont := true
		for cont {
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
					if err == nil {
						q.logger.Printf("Added torrent '%s' to rTorrent", event.Name)
					} else {
						q.logger.Printf("Failed to add '%s' error '%s'", event.Name, err)
					}
				} else if event.Op&fsnotify.Remove == fsnotify.Remove {
					for hash, path := range q.downloadQueue {
						if path == event.Name {
							q.logger.Printf("Removing torrent '%s' from queue", path)
							delete(q.downloadQueue, hash)
							break
						}
					}
				}
				break

			case err = <-q.fsWatcher.Errors:
				q.logger.Printf("Error while watching folders '%s'", err)
				cont = false
				break

			case rpcReq := <-q.rpcQueue:
				switch rpcReq.method {
				case "download_status":
					status := q.getDownloadStatus(downloadsRunning)
					rpcReq.replyChan <- status
				}
				break

			case <-stop:
				finished = true
				cont = false
				break

			default:
				time.Sleep(time.Second)
				cont = false
				break
			}
		}

		if finished {
			break
		}

		if time.Now().Sub(lastUpdateTime) >= q.config.TorrentListUpdateInterval {
			err := q.updateTorrentList()
			if err == nil {
				lastUpdateTime = time.Now()
			} else {
				q.logger.Printf("Failed to update torrent list from rTorrent: '%s'", err)
			}
		}

		if len(downloadsRunning) == 0 {
			// close the sftp connection since it is not being used
			if q.sftpClient != nil {
				q.sftpClient.Close()
				q.sftpClient = nil
			}
		} else {
			cont = true
			for cont {
				select {
				case finishedHash := <-downloadedHashes:
					if finishedHash == "" {
						break
					}

					if finishedHash[0] == '!' {
						finishedHash = finishedHash[1:]
					} else {
						delete(q.downloadQueue, finishedHash)
					}

					delete(downloadsRunning, finishedHash)
					break

				default:
					cont = false
					break
				}
			}
		}

		if uint(len(downloadsRunning)) < q.config.ConcurrentDownloads {
			for _, torrent := range q.getFinishedTorrents() {
				if _, exists := downloadsRunning[torrent.Hash]; exists {
					continue
				}

				if q.sftpClient == nil {
					q.sftpClient, err = newSftpClient(q.config)
					if err != nil {
						q.logger.Println("Failed to connect to sftp: ", err)
						q.sftpClient = nil
						continue
					}
				}

				torrentFilePath := q.downloadQueue[torrent.Hash]

				skip := false
				if q.config.CheckDiskSpace {
					diskSpacePaths := []string{q.config.WatchDownloadPaths[filepath.Dir(torrentFilePath)]}

					if q.config.TempDownloadPath != "" {
						diskSpacePaths = append(diskSpacePaths, q.config.TempDownloadPath)
					}

					downloadSizes := uint64(torrent.Size)
					for _, dTorrent := range downloadsRunning {
						downloadSizes += uint64(dTorrent.size)
					}

					for _, path := range diskSpacePaths {
						fsStat, err := disk.Usage(path)
						if err != nil {
							q.logger.Printf("Failed to check disk space on '%s' for '%s' (%s): %s", path, torrent.Name, torrentFilePath, err)
							continue
						}

						if q.config.MinDiskSpace == 0 {
							if fsStat.Free > downloadSizes {
								continue
							}

							q.logger.Printf("Not downloading '%s' (%s) not enough disk space, only %d bytes free on '%s'", torrent.Name, torrentFilePath, fsStat.Free, path)
							skip = true
							break
						} else {
							if fsStat.Free > downloadSizes && (fsStat.Free-downloadSizes) > q.config.MinDiskSpace {
								continue
							}

							q.logger.Printf("Not downloading '%s' (%s) minimum disk space (%d) reached on '%s'", torrent.Name, torrentFilePath, q.config.MinDiskSpace, path)
							skip = true
							break
						}
					}
				}

				if skip {
					continue
				}

				go func(torrent rtorrent.Torrent, torrentPath string, hashChan chan<- string) {
					err := q.downloadTorrent(torrent, torrentPath)
					if err != nil {
						q.logger.Printf("Failed to download '%s' (%s): %s", torrent.Name, torrentPath, err)
						hashChan <- "!" + torrent.Hash
						return
					}

					hashChan <- torrent.Hash
				}(torrent, torrentFilePath, downloadedHashes)

				destDownloadDir := q.config.WatchDownloadPaths[filepath.Dir(torrentFilePath)]
				downloadDir := filepath.Join(q.config.TempDownloadPath, destDownloadDir)
				downloadsRunning[torrent.Hash] = downloadInfo{
					path: filepath.Join(downloadDir, torrent.Name),
					size: torrent.Size,
				}

				if uint(len(downloadsRunning)) == q.config.ConcurrentDownloads {
					break
				}
			}
		}
	}
}
