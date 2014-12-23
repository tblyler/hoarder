// Send and receive files via SFTP using multiple download streams concurrently (for downloads).
package main

import (
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// Keeps track of the connection information.
type Sync struct {
	sshClient *ssh.Client
	sftpClients []*sftp.Client
	sftpClientCount int
}

// Create a new Sync object, connect to the SSH server, and create sftp clients
func NewSync(threads string, user string, pass string, server string, port string) (*Sync, error) {
	// convert the threads input to an int
	client_count, err := strconv.Atoi(threads)
	if err != nil {
		return nil, err
	}

	if client_count < 1 {
		return nil, errors.New("Must have a thread count >= 1")
	}

	ssh_client, err := newSSHClient(user, pass, server, port)
	if err != nil {
		return nil, err
	}

	// initialize a total of client_count sftp clients
	sftp_clients := make([]*sftp.Client, client_count)
	for i := 0; i < client_count; i++ {
		sftp_client, err := sftp.NewClient(ssh_client)
		if err != nil {
			return nil, err
		}

		sftp_clients[i] = sftp_client
	}

	return &Sync{ssh_client, sftp_clients, client_count}, nil
}

// Create a new SSH client instance and confirm that we can make sessions
func newSSHClient(user string, pass string, server string, port string) (*ssh.Client, error) {
	ssh_config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
		},
	}

	ssh_client, err := ssh.Dial("tcp", server + ":" + port, ssh_config)

	if err != nil {
		return ssh_client, err
	}

	session, err := ssh_client.NewSession()
	if err != nil {
		return ssh_client, err
	}

	defer session.Close()

	return ssh_client, err
}

// Send a list of files in the format of {"source_path": "destination"} to the SSH server.
// This does not handle directories.
func (s Sync) SendFiles(files map[string]string) error {
	return SendFiles(s.sftpClients[0], files)
}

// Send a list of files in the format of {"source_path": "destination"} to the SSH server.
// This does not handle directories.
func SendFiles(sftp_client *sftp.Client, files map[string]string) error {
	for source_file, destination_file := range files {
		// 512KB buffer for reading/sending data
		data := make([]byte, 524288)

		// Open file that we will be sending
		source_data, err := os.Open(source_file)
		if err != nil {
			log.Println("SendFiles: Failed to open source file " + source_file)
			return err
		}

		// Get the info of the file that we will be sending
		source_stat, err := source_data.Stat()
		if err != nil {
			log.Println("SendFiles: Failed to stat source file " + source_file)
			return err
		}

		// Extract the size of the file that we will be sending
		source_size := source_stat.Size() 
		// Create the destination file for the source file we're sending
		new_file, err := sftp_client.Create(destination_file)
		if err != nil {
			log.Println("SendFiles: Failed to create destination file " + destination_file)
			return err
		}

		// Track our position in reading/writing the file
		var current_position int64 = 0
		for current_position < source_size {
			// If the next iteration will be greater than the file size, reduce to the data size
			if current_position + int64(len(data)) > source_size {
				data = make([]byte, source_size - current_position)
			}

			// Read data from the source file
			read, err := source_data.Read(data)
			if err != nil {
				// If it's the end of the file and we didn't read anything, break
				if err == io.EOF {
					if read == 0 {
						break
					}
				} else {
					return err
				}
			}

			// Write the data from the source file to the destination file
			_, err = new_file.Write(data)
			if err != nil {
				return err
			}

			// Update the current position in the file
			current_position += int64(read)
		}

		// close the source file
		err = source_data.Close()
		if err != nil {
			return err
		}

		// close the destination file
		err = new_file.Close()
		if err != nil {
			return err
		}
	}

	return nil
}

// Get a file from the source_file path to be stored in destination_file path
func (s Sync) GetFile(source_file string, destination_file string) error {
	// Store channels for all the concurrent download parts
	channels := make([]chan error, s.sftpClientCount)

	// Make channels for all the concurrent downloads
	for i := 0; i < s.sftpClientCount; i++ {
		channels[i] = make(chan error)
	}

	// Start the concurrent downloads
	for i := 0; i < s.sftpClientCount; i++ {
		go GetFile(s.sftpClients[i], source_file, destination_file, i + 1, s.sftpClientCount, channels[i])
	}

	// Block until all downloads are completed or one errors
	for _, channel := range channels {
		err := <- channel
		if err != nil {
			return err
		}
	}

	return nil
}

// Get a file from the source_file path to be stored in destination_file path.
// worker_number and work_total are not zero indexed, but 1 indexed
func GetFile(sftp_client *sftp.Client, source_file string, destination_file string, worker_number int, worker_total int, com chan <- error) error {
	// Open source_data for reading
	source_data, err := sftp_client.OpenFile(source_file, os.O_RDONLY)
	if err != nil {
		com <- err
		return err
	}

	// Get info for source_data
	stat, err := source_data.Stat()
	if err != nil {
		com <- err
		return err
	}

	// Extract the size of source_data
	stat_size := stat.Size()
	// Calculate which byte to start reading data from
	var start int64
	if worker_number == 1 {
		start = 0
	} else {
		start = (stat_size * int64(worker_number - 1)) / int64(worker_total)
	}

	// Calculate which byte to stop reading data from
	var stop int64
	if worker_number == worker_total {
		stop = stat_size
	} else {
		stop = (stat_size * int64(worker_number)) / int64(worker_total)
	}

	// Create the new file for writing
	new_file, err := os.OpenFile(destination_file, os.O_WRONLY | os.O_CREATE, 0777)
	if err != nil {
		com <- err
		return err
	}

	// Seek to the computed start point
	offset, err := source_data.Seek(start, 0)
	if err != nil {
		com <- err
		return err
	}

	// Seeking messed up real bad
	if offset != start {
		err = errors.New("Returned incorrect offset for source " + source_file)
		com <- err
		return err
	}

	// Seek to the computed start point
	offset, err = new_file.Seek(start, 0)
	if err != nil {
		com <- err
		return err
	}

	// Seeking messed up real bad
	if offset != start {
		err = errors.New("Return incorrect offset for destination " + destination_file)
		com <- err
		return err
	}

	// 512KB chunks
	var data_size int64 = 524288
	// Change the size if the chunk is larger than the file
	chunk_difference := stop - start
	if chunk_difference < data_size {
		data_size = chunk_difference
	}

	// Initialize the buffer for reading/writing
	data := make([]byte, data_size)

	for current_size := start; current_size < stop; current_size += data_size {
		// Adjust the size of the buffer if the next iteration will be greater than what has yet to be read
		if current_size + data_size > stop {
			data_size = stop - current_size
			data = make([]byte, data_size)
		}

		// Read the chunk
		read, err := source_data.Read(data)
		if err != nil {
			// Exit the loop if we're at the end of the file and no data was read
			if err == io.EOF {
				if read == 0 {
					break
				}
			} else {
				com <- err
				return err
			}
		}

		// Write the chunk
		_, err = new_file.Write(data)
		if err != nil {
			com <- err
			return err
		}
	}

	// Close out the files
	err = source_data.Close()
	if err != nil {
		com <- err
		return err
	}

	err = new_file.Close()
	if err != nil {
		com <- err
		return err
	}

	com <- nil
	return nil
}

// Get a given directory or file defined by source_path and save it to destination_path
func (s Sync) GetPath(source_path string, destination_path string) error {
	// Get all the dirs and files underneath source_path
	dirs, files, err := s.getChildren(source_path)

	// Remove the trailing slash if it exists
	if source_path[len(source_path) - 1] == '/' {
		source_path = source_path[:len(source_path) - 1]
	}

	// Get the parent path of source_path
	source_base := filepath.Dir(source_path)
	source_base_len := len(source_base)

	// Make all the directories in destination_path
	for _, dir := range dirs {
		dir = filepath.Join(destination_path, filepath.FromSlash(dir[source_base_len:]))
		err = os.MkdirAll(dir, 0777)
		if err != nil {
			return err
		}
	}

	// Get all the files and place them in destination_path
	for _, file := range files {
		new_file := filepath.Join(destination_path, filepath.FromSlash(file[source_base_len:]))
		err = s.GetFile(file, new_file)
		if err != nil {
			return err
		}
	}

	return nil
}

// Get the directories and files underneath a given sftp root path
func (s Sync) getChildren(root string) ([]string, []string, error) {
	// Used to walk through the path
	walker := s.sftpClients[0].Walk(root)

	// Keep track of the directories
	dirs := make([]string, 0)
	// Keep track of the files
	files := make([]string, 0)

	// Walk through the files and directories
	for walker.Step() {
		err := walker.Err()
		if err != nil {
			return nil, nil, err
		}

		stat := walker.Stat()
		if stat.IsDir() {
			dirs = append(dirs, walker.Path())
		} else {
			files = append(files, walker.Path())
		}
	}

	err := walker.Err()
	if err != nil {
		return nil, nil, err
	}

	return dirs, files, nil
}

// Determine if a directory, file, or link exists
func (s Sync) Exists(path string) (bool, error) {
	_, err := s.sftpClients[0].Lstat(path)

	if err != nil {
		if err.Error() == "sftp: \"No such file\" (SSH_FX_NO_SUCH_FILE)" {
			return false, nil
		}

		return false, err
	}

	return true, nil
}
