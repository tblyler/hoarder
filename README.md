[![Build Status](https://travis-ci.org/tblyler/hoarder.svg?branch=master)](https://travis-ci.org/tblyler/hoarder)
# Hoarder
Uploads .torrent files from a local "blackhole" to a remote (SSH) rtorrent watch folder. From there, rtorrent is polled over XMLRPC as to whether the torrent is completed. Finally, the files are downloaded over a multithreaded SSH connection and saved to the local machine. The blackhole is used as a queue and will have its .torrent files deleted.

# Installation
## Manual
1. Install [Go](https://golang.org) for your Operating System
2. Run `$ go get -u github.com/tblyler/hoarder/cmd/hoarder`
3. If your `GOPATH` is in your `PATH`, run `$ hoarder -config $PATH_TO_HOARDER_CONF`

## Precompiled
1. Go to the [Hoarder releases page](https://github.com/tblyler/hoarder/releases)
2. Look at whichever release you're interested in
3. Download a precompiled binary for the given operating system of your choice
4. Make the binary executable and run

# Configuration
## Example
```yaml
# The file mode to use for downloaded files and directories
file_download_filemode: 0777

# The correlation of .torrent file paths and where their contents should be downloaded to"
watch_to_download_paths:
  /home/tblyler/torrent_files/tv: /home/tblyler/Downloads/tv
  /home/tblyler/torrent_files/movies: /home/tblyler/Downloads/movies
  /home/tblyler/torrent_files: /home/tblyler/Downloads

# The root path to temporarily download to and then move to the folder in the setting above. The destination path is created underneath the temp_download_path
temp_download_path: /home/tblyler/tempDownloads

# If defined, the finished .torrent files finished are moved to their respected path here. Otherwise, they are deleted.
watch_to_finish_path:
  /home/tblyler/torrent_files/tv: /home/tblyler/completed_torrent_files/tv
  /home/tblyler/torrent_files: /home/tblyler/completed_torrent_files

# The time in nano seconds to update the list of torrents and their statuses in rTorrent
rtorrent_update_interval: 60000000000

# The number of concurrent completed torrent downloads to have at one time
download_jobs: 2

# Whether or not to attempt to resume a previously interrupted download
resume_downloads: true

# Path to the unix socket file that hoarder uses for RPC
rpc_socket_path: /tmp/hoarder.sock

# Whether or not to see if there is enough disk space before starting a download
check_disk_space: true

# (must have check_disk_space set to true) Minimum disk space to have after completed downloads (measured in bytes, 0 to disable check)
min_disk_space: 5368709120

rtorrent:
  # The address to the rtorrent XMLRPC endpoint
  addr: https://mycoolrtorrentserver.com/XMLRPC

  # true to ignore the certificate authenticity; false to honor it
  insecure_cert: false

  # The HTTP Basic auth username to use for rtorrent's XMLRPC
  username: JohnDoe

  # The HTTP Basic auth password to use for rtorrent's XMLRPC
  password: "correct horse battery staple"

ssh:
  # The ssh username to use for getting finished torrents from the remote host
  username: JohnDoe

  # You may choose to use an ssh key or ssh password. If both are supplied, the password will not be used.

  # The SSH password to use for SSH authentication
  password: "correct horse battery staple SSH"

  # The path to the private SSH key for SSH authentication
  privkey_path: /home/tblyler/.ssh/id_rsa

  # The SSH address to connect to
  addr: "mysshserver.com:22"

  # The time in nano seconds to wait for an SSH connection attempt
  connect_timeout: 30000000000
```
