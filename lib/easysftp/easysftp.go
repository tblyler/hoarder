package easysftp

import (
	"errors"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

// ClientConfig maintains all of the configuration info to connect to a SSH host
type ClientConfig struct {
	Username string
	Host     string
	KeyPath  string
	Password string
	Timeout  time.Duration
	FileMode os.FileMode
}

// Client communicates with the SFTP to download files/pathes
type Client struct {
	sshClient *ssh.Client
	config    *ClientConfig
}

// Connect to a host with this given config
func Connect(config *ClientConfig) (*Client, error) {
	var auth []ssh.AuthMethod
	if config.KeyPath != "" {
		privKey, err := ioutil.ReadFile(config.KeyPath)
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey(privKey)
		if err != nil {
			return nil, err
		}

		auth = append(auth, ssh.PublicKeys(signer))
	}

	if len(auth) == 0 {
		if config.Password == "" {
			return nil, errors.New("Missing password or key for SSH authentication")
		}

		auth = append(auth, ssh.Password(config.Password))
	}

	sshClient, err := ssh.Dial("tcp", config.Host, &ssh.ClientConfig{
		User:    config.Username,
		Auth:    auth,
		Timeout: config.Timeout,
	})
	if err != nil {
		return nil, err
	}

	return &Client{
		sshClient: sshClient,
		config:    config,
	}, nil
}

// Close the underlying SSH conection
func (c *Client) Close() error {
	return c.sshClient.Close()
}

func (c *Client) newSftpClient() (*sftp.Client, error) {
	return sftp.NewClient(c.sshClient)
}

// Stat gets information for the given path
func (c *Client) Stat(path string) (os.FileInfo, error) {
	sftpClient, err := c.newSftpClient()
	if err != nil {
		return nil, err
	}

	defer sftpClient.Close()

	return sftpClient.Stat(path)
}

// Lstat gets information for the given path, if it is a symbolic link, it will describe the symbolic link
func (c *Client) Lstat(path string) (os.FileInfo, error) {
	sftpClient, err := c.newSftpClient()
	if err != nil {
		return nil, err
	}

	defer sftpClient.Close()

	return sftpClient.Lstat(path)
}

// Download a file from the given path to the output writer
func (c *Client) Download(path string, output io.Writer) error {
	sftpClient, err := c.newSftpClient()
	if err != nil {
		return err
	}

	defer sftpClient.Close()

	info, err := sftpClient.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return errors.New("Unable to use easysftp.Client.Download for dir: " + path)
	}

	remote, err := sftpClient.Open(path)
	if err != nil {
		return err
	}

	defer remote.Close()

	_, err = io.Copy(output, remote)
	return err
}

// Mirror downloads an entire folder (recursively) or file underneath the given localParentPath
func (c *Client) Mirror(path string, localParentPath string) error {
	sftpClient, err := c.newSftpClient()
	if err != nil {
		return err
	}

	defer sftpClient.Close()

	info, err := sftpClient.Stat(path)
	if err != nil {
		return err
	}

	// download the file
	if !info.IsDir() {
		sftpClient.Close()
		localPath := filepath.Join(localParentPath, info.Name())
		localInfo, err := os.Stat(localPath)
		if os.IsExist(err) && localInfo.IsDir() {
			err = os.RemoveAll(localPath)
			if err != nil {
				return err
			}
		}

		file, err := os.OpenFile(
			localPath,
			os.O_RDWR|os.O_CREATE|os.O_TRUNC,
			c.config.FileMode,
		)
		if err != nil {
			return err
		}

		defer file.Close()

		return c.Download(path, file)
	}

	// download the whole directory recursively
	walker := sftpClient.Walk(path)
	remoteParentPath := filepath.Dir(path)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			return err
		}

		info := walker.Stat()

		relPath, err := filepath.Rel(remoteParentPath, walker.Path())
		if err != nil {
			return err
		}

		localPath := filepath.Join(localParentPath, relPath)

		// if we have something at the download path delete it if it is a directory
		// and the remote is a file and vice a versa
		localInfo, err := os.Stat(localPath)
		if os.IsExist(err) {
			if localInfo.IsDir() {
				if info.IsDir() {
					continue
				}

				err = os.RemoveAll(localPath)
				if err != nil {
					return err
				}
			} else if info.IsDir() {
				err = os.Remove(localPath)
				if err != nil {
					return err
				}
			}
		}

		if info.IsDir() {
			err = os.MkdirAll(localPath, c.config.FileMode)
			if err != nil {
				return err
			}

			continue
		}

		remoteFile, err := sftpClient.Open(walker.Path())
		if err != nil {
			return err
		}

		localFile, err := os.OpenFile(localPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, c.config.FileMode)
		if err != nil {
			remoteFile.Close()
			return err
		}

		_, err = io.Copy(localFile, remoteFile)
		remoteFile.Close()
		localFile.Close()

		if err != nil {
			return err
		}
	}

	return nil
}
