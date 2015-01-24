// Send and receive files via SFTP using multiple download streams concurrently (for downloads).
package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// NoProgress no progress for current file
	NoProgress int64 = -1
)

// Sync Keeps track of the connection information.
type Sync struct {
	sshClient       *ssh.Client
	sftpClients     []*sftp.Client
	sftpClientCount int
}

// NewSync Create a new Sync object, connect to the SSH server, and create sftp clients
func NewSync(threads string, user string, pass string, server string, port string) (*Sync, error) {
	// convert the threads input to an int
	clientCount, err := strconv.Atoi(threads)
	if err != nil {
		return nil, err
	}

	if clientCount < 1 {
		return nil, errors.New("Must have a thread count >= 1")
	}

	sshClient, err := newSSHClient(user, pass, server, port)
	if err != nil {
		return nil, err
	}

	// initialize a total of client_count sftp clients
	sftpClients := make([]*sftp.Client, clientCount)
	for i := 0; i < clientCount; i++ {
		sftpClient, err := sftp.NewClient(sshClient)
		if err != nil {
			return nil, err
		}

		sftpClients[i] = sftpClient
	}

	return &Sync{sshClient, sftpClients, clientCount}, nil
}

// Close Closes all of the ssh and sftp connections to the SSH server.
func (s Sync) Close() error {
	var returnError error
	for i := 0; i < s.sftpClientCount; i++ {
		err := s.sftpClients[i].Close()
		if err != nil {
			returnError = err
		}
	}

	err := s.sshClient.Close()
	if err != nil {
		return err
	}

	return returnError
}

// Create a new SSH client instance and confirm that we can make sessions
func newSSHClient(user string, pass string, server string, port string) (*ssh.Client, error) {
	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
		},
	}

	sshClient, err := ssh.Dial("tcp", server+":"+port, sshConfig)

	if err != nil {
		return sshClient, err
	}

	session, err := sshClient.NewSession()
	if err != nil {
		return sshClient, err
	}

	defer session.Close()

	return sshClient, err
}

// SendFiles Send a list of files in the format of {"source_path": "destination"} to the SSH server. This does not handle directories.
func (s Sync) SendFiles(files map[string]string) error {
	return SendFiles(s.sftpClients[0], files)
}

// SendFiles Send a list of files in the format of {"source_path": "destination"} to the SSH server. This does not handle directories.
func SendFiles(sftpClient *sftp.Client, files map[string]string) error {
	for sourceFile, destinationFile := range files {
		// 512KB buffer for reading/sending data
		data := make([]byte, 524288)

		// Open file that we will be sending
		sourceData, err := os.Open(sourceFile)
		if err != nil {
			log.Println("SendFiles: Failed to open source file " + sourceFile)
			return err
		}

		// Get the info of the file that we will be sending
		sourceStat, err := sourceData.Stat()
		if err != nil {
			log.Println("SendFiles: Failed to stat source file " + sourceFile)
			return err
		}

		// Extract the size of the file that we will be sending
		sourceSize := sourceStat.Size()
		// Create the destination file for the source file we're sending
		newFile, err := sftpClient.Create(destinationFile)
		if err != nil {
			log.Println("SendFiles: Failed to create destination file " + destinationFile)
			return err
		}

		// Track our position in reading/writing the file
		var currentPosition int64
		for currentPosition < sourceSize {
			// If the next iteration will be greater than the file size, reduce to the data size
			if currentPosition+int64(len(data)) > sourceSize {
				data = make([]byte, sourceSize-currentPosition)
			}

			// Read data from the source file
			read, err := sourceData.Read(data)
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
			_, err = newFile.Write(data)
			if err != nil {
				return err
			}

			// Update the current position in the file
			currentPosition += int64(read)
		}

		// close the source file
		err = sourceData.Close()
		if err != nil {
			return err
		}

		// close the destination file
		err = newFile.Close()
		if err != nil {
			return err
		}
	}

	return nil
}

// GetFile Get a file from the source_file path to be stored in destination_file path
func (s Sync) GetFile(sourceFile string, destinationFile string) error {
	// Store channels for all the concurrent download parts
	channels := make([]chan error, s.sftpClientCount)

	// Make channels for all the concurrent downloads
	for i := 0; i < s.sftpClientCount; i++ {
		channels[i] = make(chan error)
	}

	// Start the concurrent downloads
	for i := 0; i < s.sftpClientCount; i++ {
		go GetFile(s.sftpClients[i], sourceFile, destinationFile, i+1, s.sftpClientCount, channels[i])
	}

	// Block until all downloads are completed or one errors
	allDone := false
	for !allDone {
		allDone = true
		for i, channel := range channels {
			if channel == nil {
				continue
			}

			select {
			case err := <-channel:
				if err != nil {
					return err
				}

				channels[i] = nil
				break
			default:
				// still running
				if allDone {
					allDone = false
				}
				break
			}
		}

		time.Sleep(time.Second)
	}

	err := destroyProgress(destinationFile)
	if err != nil {
		return err
	}

	return nil
}

// GetFile Get a file from the source_file path to be stored in destination_file path.
// worker_number and work_total are not zero indexed, but 1 indexed
func GetFile(sftpClient *sftp.Client, sourceFile string, destinationFile string, workerNumber int, workerTotal int, com chan<- error) error {
	// Open source_data for reading
	sourceData, err := sftpClient.OpenFile(sourceFile, os.O_RDONLY)
	if err != nil {
		com <- err
		return err
	}

	// Get info for source_data
	stat, err := sourceData.Stat()
	if err != nil {
		com <- err
		return err
	}

	// Extract the size of source_data
	statSize := stat.Size()

	// Calculate which byte to start reading data from
	start, err := getProgress(destinationFile, workerNumber)
	if err != nil {
		com <- err
		return err
	}

	if start == NoProgress {
		if workerNumber == 1 {
			start = 0
		} else {
			start = (statSize * int64(workerNumber-1)) / int64(workerTotal)
		}
	}

	// Calculate which byte to stop reading data from
	var stop int64
	if workerNumber == workerTotal {
		stop = statSize
	} else {
		stop = (statSize * int64(workerNumber)) / int64(workerTotal)
	}

	// Create the new file for writing
	newFile, err := os.OpenFile(destinationFile, os.O_WRONLY|os.O_CREATE, 0777)
	if err != nil {
		com <- err
		return err
	}

	// Seek to the computed start point
	offset, err := sourceData.Seek(start, 0)
	if err != nil {
		com <- err
		return err
	}

	// Seeking messed up real bad
	if offset != start {
		err = errors.New("Returned incorrect offset for source " + sourceFile)
		com <- err
		return err
	}

	// Seek to the computed start point
	offset, err = newFile.Seek(start, 0)
	if err != nil {
		com <- err
		return err
	}

	// Seeking messed up real bad
	if offset != start {
		err = errors.New("Return incorrect offset for destination " + destinationFile)
		com <- err
		return err
	}

	// 512KB chunks
	var dataSize int64 = 524288
	// Change the size if the chunk is larger than the file
	chunkDifference := stop - start
	if chunkDifference < dataSize {
		dataSize = chunkDifference
	}

	// Initialize the buffer for reading/writing
	data := make([]byte, dataSize)
	var currentSize int64
	for currentSize = start; currentSize < stop; currentSize += dataSize {
		err = updateProgress(destinationFile, currentSize, workerNumber)
		if err != nil {
			com <- err
			return err
		}

		// Adjust the size of the buffer if the next iteration will be greater than what has yet to be read
		if currentSize+dataSize > stop {
			dataSize = stop - currentSize
			data = make([]byte, dataSize)
		}

		// Read the chunk
		read, err := sourceData.Read(data)
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
		_, err = newFile.Write(data)
		if err != nil {
			com <- err
			return err
		}
	}

	err = updateProgress(destinationFile, currentSize, workerNumber)
	if err != nil {
		com <- err
		return err
	}

	// Close out the files
	err = sourceData.Close()
	if err != nil {
		com <- err
		return err
	}

	err = newFile.Close()
	if err != nil {
		com <- err
		return err
	}

	com <- nil
	return nil
}

// GetPath Get a given directory or file defined by source_path and save it to destination_path
func (s Sync) GetPath(sourcePath string, destinationPath string) error {
	// Get all the dirs and files underneath source_path
	dirs, files, err := s.getChildren(sourcePath)

	// Remove the trailing slash if it exists
	if sourcePath[len(sourcePath)-1] == '/' {
		sourcePath = sourcePath[:len(sourcePath)-1]
	}

	// Get the parent path of source_path
	sourceBase := filepath.Dir(sourcePath)
	sourceBaseLen := len(sourceBase)

	// Make all the directories in destination_path
	for _, dir := range dirs {
		dir = filepath.Join(destinationPath, filepath.FromSlash(dir[sourceBaseLen:]))
		err = os.MkdirAll(dir, 0777)
		if err != nil {
			return err
		}
	}

	// Get all the files and place them in destination_path
	for _, file := range files {
		newFile := filepath.Join(destinationPath, filepath.FromSlash(file[sourceBaseLen:]))
		err = s.GetFile(file, newFile)
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
	var dirs []string
	// Keep track of the files
	var files []string

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

// Exists Determine if a directory, file, or link exists
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

func getProgress(filePath string, workerNumber int) (int64, error) {
	file, err := os.Open(getProgressPath(filePath))
	if err != nil {
		if os.IsNotExist(err) {
			return NoProgress, nil
		}

		return 0, err
	}

	fileStat, err := file.Stat()
	if err != nil {
		return 0, err
	}

	if fileStat.Size() == 0 {
		return NoProgress, nil
	}

	var progress int64
	progressSize := int64(binary.Size(progress))
	offset := progressSize * int64(workerNumber-1)

	realOffset, err := file.Seek(offset, os.SEEK_SET)
	if err != nil {
		return 0, err
	}

	if realOffset != offset {
		return 0, errors.New("getProgress: Tried to seek to " + string(offset) + " but got " + string(realOffset) + " instead")
	}

	progressData := make([]byte, progressSize)

	read, err := file.Read(progressData)
	if err != nil {
		if err == io.EOF {
			return NoProgress, nil
		}

		return 0, err
	}

	if int64(read) != progressSize {
		return NoProgress, nil
	}

	err = binary.Read(bytes.NewReader(progressData), binary.BigEndian, &progress)
	if err != nil {
		return 0, err
	}

	return progress, nil
}

func updateProgress(filePath string, written int64, workerNumber int) error {
	file, err := os.OpenFile(getProgressPath(filePath), os.O_WRONLY|os.O_CREATE, 0777)
	if err != nil {
		return err
	}

	writtenSize := int64(binary.Size(written))
	offset := writtenSize * int64(workerNumber-1)

	realOffset, err := file.Seek(offset, os.SEEK_SET)
	if err != nil {
		return err
	}

	if realOffset != offset {
		return errors.New("updateProgress: Tried to seek to " + string(offset) + " but got " + string(realOffset) + " instead")
	}

	return binary.Write(file, binary.BigEndian, written)
}

func destroyProgress(filePath string) error {
	err := os.Remove(getProgressPath(filePath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func getProgressPath(filePath string) string {
	return filepath.Join(filepath.Dir(filePath), "."+filepath.Base(filePath)+".progress")
}
