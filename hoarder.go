// Watch a torrent directory, poll rtorrent, and download completed torrents over SFTP.
package main

import (
	"errors"
	"github.com/adampresley/sigint"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Load information from a given config file config_path
func loadConfig(config_path string) (map[string]string, error) {
	file, err := os.Open(config_path)
	if err != nil {
		log.Println("Failed to open configuration file " + config_path)
		return nil, err
	}

	data, err := ioutil.ReadAll(file)
	if err != nil {
		log.Println("Failed to read configuration file " + config_path)
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
		sep_position := strings.Index(line, ": \"")
		if sep_position == -1 {
			continue
		}

		config[line[:sep_position]] = line[sep_position + 3:len(line) - 1]
	}

	return config, nil
}

// Checker routine to see if torrents are completed
func checker(config map[string]string, checker_chan <- chan map[string]string, com chan <- error) error {
	for {
		torrent_info := <-checker_chan

		log.Println("Started checking " + torrent_info["torrent_path"])

		torrent, err := NewTorrent(config["xml_user"], config["xml_pass"], config["xml_address"], torrent_info["torrent_path"])
		if err != nil {
			log.Println("Failed to initialize torrent for " + torrent_info["torrent_path"] + ": " + err.Error())
			continue
		}

		syncer, err := NewSync(config["threads"], config["ssh_user"], config["ssh_pass"], config["ssh_server"], config["ssh_port"])
		if err != nil {
			log.Println("Failed to create a new sync: " + err.Error())
			com <- err
			return err
		}

		destination_torrent := filepath.Join(config["remote_torrent_dir"], filepath.Base(torrent.path))
		exists, err := syncer.Exists(destination_torrent)
		if err != nil {
			log.Println("Failed to see if " + torrent_info["torrent_path"] + " already exists on the server: " + err.Error())
			com <- err
			return err
		}

		if !exists {
			err = syncer.SendFiles(map[string]string{torrent.path: destination_torrent})
			if err != nil {
				log.Println("Failed to send " + torrent.path + " to the server: " + err.Error())
				com <- err
				return err
			}

			// continue because rtorrent more than likely will not finish the torrent by the next call
			continue
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

			remote_download_path := filepath.Join(config["remote_download_dir"], name)
			exists, err := syncer.Exists(remote_download_path)
			if err != nil {
				log.Println("Failed to see if " + remote_download_path + " exists: " + err.Error())
				com <- err
				return err
			}

			// file/dir to downlaod does not exist!
			if !exists {
				err = errors.New(remote_download_path + " does not exist on remote server")
				com <- err
				return err
			}

			completed_destination := filepath.Join(config["local_download_dir"], name)

			_, err = os.Stat(completed_destination)
			if err == nil {
				err = errors.New(completed_destination + " already exists, not downloading")
				continue
			} else if !os.IsNotExist(err) {
				log.Println("Failed to stat: " + completed_destination + ": " + err.Error())
				com <- err
				return err
			}

			err = syncer.GetPath(remote_download_path, config["temp_download_dir"])
			if err != nil {
				log.Println("Failed to download " + remote_download_path + ": " + err.Error())
				com <- err
				return err
			}

			log.Println("Successfully downloaded " + name)

			temp_destination := filepath.Join(config["temp_download_dir"], name)

			err = os.Rename(temp_destination, completed_destination)
			if err != nil {
				log.Println("Failed to move " + temp_destination + " to " + completed_destination + ": " + err.Error())
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
func scanner(config map[string]string, checker_chan chan <- map[string]string, com chan <- error) error {
	watch_dirs := map[string]string{config["local_torrent_dir"]: config["local_download_dir"]}
	dir_contents, err := ioutil.ReadDir(config["local_torrent_dir"])

	if err != nil {
		com <- err
		return err
	}

	for _, file := range dir_contents {
		if file.IsDir() {
			watch_dirs[filepath.Join(config["local_torrent_dir"], file.Name())] = filepath.Join(config["local_download_dir"], file.Name())
		}
	}

	for {
		for watch_dir, download_dir := range watch_dirs {
			torrent_files, err := ioutil.ReadDir(watch_dir)
			if err != nil {
				com <- err
				return err
			}

			for _, torrent_file := range torrent_files {
				if torrent_file.IsDir() {
					// skip because we don't do more than one level of watching
					continue
				}

				checker_chan <- map[string]string{
					"torrent_path": filepath.Join(watch_dir, torrent_file.Name()),
					"local_download_dir": download_dir,
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

	log.Println("Reading configuration file")
	config, err := loadConfig("hoarder.conf")
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	log.Println("Successfully read configuration file")

	checker_chan := make(chan map[string]string, 50)

	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	log.Println("Starting the scanner routine")
	scanner_com := make(chan error)
	go scanner(config, checker_chan, scanner_com)

	log.Println("Starting the checker routine")
	checker_com := make(chan error)
	go checker(config, checker_chan, checker_com)

	for {
		select {
		case err := <-scanner_com:
			if err != nil {
				log.Println("Scanner failed: " + err.Error())
				os.Exit(1)
			}
		case err := <-checker_com:
			if err != nil {
				log.Println("Checker failed: " + err.Error())
				os.Exit(1)
			}
		}

		time.Sleep(time.Second * 5)
	}
}
