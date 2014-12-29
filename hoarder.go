// Watch a torrent directory, poll rtorrent, and download completed torrents over SFTP.
package main

import (
	"errors"
	"flag"
	"github.com/adampresley/sigint"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Load information from a given config file config_path
func loadConfig(configPath string) (map[string]string, error) {
	file, err := os.Open(configPath)
	if err != nil {
		log.Println("Failed to open configuration file " + configPath)
		return nil, err
	}

	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Println("Failed to read configuration file " + configPath)
		return nil, err
	}

	config := make(map[string]string)

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Ignore comments
		if len(line) <= 2 || line[:2] == "//" {
			continue
		}

		// Ignore malformed lines
		sepPosition := strings.Index(line, ": \"")
		if sepPosition == -1 {
			continue
		}

		config[line[:sepPosition]] = line[sepPosition + 3:len(line) - 1]
	}

	return config, nil
}

// Checker routine to see if torrents are completed
func checker(config map[string]string, checkerChan <- chan map[string]string, com chan <- error) error {
	for {
		torrentInfo := <-checkerChan

		log.Println("Started checking " + torrentInfo["torrent_path"])

		torrent, err := NewTorrent(config["xml_user"], config["xml_pass"], config["xml_address"], torrentInfo["torrent_path"])
		if err != nil {
			if !os.IsNotExist(err) {
				log.Println("Failed to initialize torrent for " + torrentInfo["torrent_path"] + ": " + err.Error())
			}

			continue
		}

		syncer, err := NewSync(config["threads"], config["ssh_user"], config["ssh_pass"], config["ssh_server"], config["ssh_port"])
		if err != nil {
			log.Println("Failed to create a new sync: " + err.Error())
			com <- err
			return err
		}

		completed, err := torrent.GetTorrentComplete()
		if err != nil {
			log.Println("Failed to see if " + torrent.path + " is completed: " + err.Error())
			com <- err
			return err
		}

		name, err := torrent.GetTorrentName()
		if err != nil {
			com <- err
			return err
		}

		if completed {
			log.Println(name + " is completed, starting download now")

			remoteDownloadPath := filepath.Join(config["remote_download_dir"], name)
			exists, err := syncer.Exists(remoteDownloadPath)
			if err != nil {
				log.Println("Failed to see if " + remoteDownloadPath + " exists: " + err.Error())
				com <- err
				return err
			}

			// file/dir to downlaod does not exist!
			if !exists {
				err = errors.New(remoteDownloadPath + " does not exist on remote server")
				com <- err
				return err
			}

			completedDestination := filepath.Join(torrentInfo["local_download_dir"], name)

			_, err = os.Stat(completedDestination)
			if err == nil {
				err = errors.New(completedDestination + " already exists, not downloading")
				continue
			} else if !os.IsNotExist(err) {
				log.Println("Failed to stat: " + completedDestination + ": " + err.Error())
				com <- err
				return err
			}

			err = syncer.GetPath(remoteDownloadPath, config["temp_download_dir"])
			if err != nil {
				log.Println("Failed to download " + remoteDownloadPath + ": " + err.Error())
				com <- err
				return err
			}

			log.Println("Successfully downloaded " + name)

			tempDestination := filepath.Join(config["temp_download_dir"], name)

			err = os.Rename(tempDestination, completedDestination)
			if err != nil {
				log.Println("Failed to move " + tempDestination + " to " + completedDestination + ": " + err.Error())
				com <- err
				return err
			}

			err = os.Remove(torrent.path)
			if err != nil && !os.IsNotExist(err) {
				log.Println("Failed to remove " + torrent.path + ": " + err.Error())
				com <- err
				return err
			}
		} else {
			log.Println(name + " is not completed, waiting for it to finish")
		}
	}

	com <- nil
	return nil
}

// Scanner routine to see if there are new torrent_files
func scanner(config map[string]string, checkerChan chan <- map[string]string, com chan <- error) error {
	watchDirs := map[string]string{config["local_torrent_dir"]: config["local_download_dir"]}
	dirContents, err := ioutil.ReadDir(config["local_torrent_dir"])

	if err != nil {
		com <- err
		return err
	}

	for _, file := range dirContents {
		if file.IsDir() {
			watchDirs[filepath.Join(config["local_torrent_dir"], file.Name())] = filepath.Join(config["local_download_dir"], file.Name())
		}
	}

	uploaded := make(map[string]bool)
	downloadingTorrentPath := ""
	for {
		for watchDir, downloadDir := range watchDirs {
			torrentFiles, err := ioutil.ReadDir(watchDir)
			if err != nil {
				com <- err
				return err
			}

			for _, torrentFile := range torrentFiles {
				if torrentFile.IsDir() {
					// skip because we don't do more than one level of watching
					continue
				}

				torrentPath := filepath.Join(watchDir, torrentFile.Name())

				if !uploaded[torrentPath] {
					syncer, err := NewSync("1", config["ssh_user"], config["ssh_pass"], config["ssh_server"], config["ssh_port"])
					if err != nil {
						log.Println("Failed to create a new sync: " + err.Error())
						continue
					}

					destinationTorrent := filepath.Join(config["remote_torrent_dir"], filepath.Base(torrentPath))
					exists, err := syncer.Exists(destinationTorrent)
					if err != nil {
						log.Println("Failed to see if " + torrentPath + " already exists on the server: " + err.Error())
						continue
					}

					if exists {
						uploaded[torrentPath] = true
					} else {
						err = syncer.SendFiles(map[string]string{torrentPath: destinationTorrent})
						if err == nil {
							log.Println("Successfully uploaded " + torrentPath + " to " + destinationTorrent)
							uploaded[torrentPath] = true
						} else {
							log.Println("Failed to upload " + torrentPath + " to " + destinationTorrent + ": " + err.Error())
						}

						continue
					}
				}

				downloadInfo := map[string]string{
					"torrent_path": torrentPath,
					"local_download_dir": downloadDir,
				}

				// try to send the info to the checker goroutine (nonblocking)
				select {
				case checkerChan <- downloadInfo:
					// don't keep track of completed downloads in the uploaded map
					if downloadingTorrentPath != "" {
						delete(uploaded, downloadingTorrentPath)
					}

					downloadingTorrentPath = torrentPath
					break
				default:
					break
				}
			}
		}

		time.Sleep(time.Second * 30)
	}

	com <- nil
	return nil
}

func main() {
	sigint.ListenForSIGINT(func() {
		log.Println("Quiting")
		os.Exit(1)
	})

	var configPath string
	flag.StringVar(&configPath, "config", "", "Location of the config file")
	flag.Parse()

	if configPath == "" {
		log.Println("Missing argument for configuration file path")
		flag.PrintDefaults()
		os.Exit(1)
	}

	log.Println("Reading configuration file")
	config, err := loadConfig(configPath)
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	log.Println("Successfully read configuration file")

	checkerChan := make(chan map[string]string, 50)

	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	log.Println("Starting the scanner routine")
	scannerCom := make(chan error)
	go scanner(config, checkerChan, scannerCom)

	log.Println("Starting the checker routine")
	checkerCom := make(chan error)
	go checker(config, checkerChan, checkerCom)

	for {
		select {
		case err := <-scannerCom:
			if err != nil {
				log.Println("Scanner failed: " + err.Error())
				os.Exit(1)
			}
		case err := <-checkerCom:
			if err != nil {
				log.Println("Checker failed: " + err.Error())
				os.Exit(1)
			}
		default:
			break
		}

		time.Sleep(time.Second * 5)
	}
}
