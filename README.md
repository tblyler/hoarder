[![Build Status](https://travis-ci.org/tblyler/hoarder.svg?branch=master)](https://travis-ci.org/tblyler/hoarder)
# Hoarder
Uploads .torrent files from a local "blackhole" to a remote (SSH) rtorrent watch folder. From there, rtorrent is polled over XMLRPC as to whether the torrent is completed. Finally, the files are downloaded over a multithreaded SSH connection and saved to the local machine. The blackhole is used as a queue and will have its .torrent files deleted.

# Installation
## Manual
1. Install [Go](https://golang.org) for your Operating System
2. Run `$ go get -u github.com/tblyler/hoarder/cmd/hoarder`
3. If your `GOPATH` is in your `PATH`, run `$ hoarder -config $PATH_TO_HOARDER_CONF`

# Configuration
## Example
Ignore the keys that start with an underscore, they are comments.
```json
{
    "_rtorrent_addr": "The address to the rtorrent XMLRPC endpoint",
    "rtorrent_addr": "mycoolrtorrentserver.com/XMLRPC",

    "_rtorrent_insecure_cert": "true to ignore the certificate authenticity; false to honor it",
    "rtorrent_insecure_cert": false,

    "_torrent_username": "The HTTP Basic auth username to use for rtorrent's XMLRPC",
    "rtorrent_username": "JohnDoe",

    "_rtorrent_password": "The HTTP Basic auth password to use for rtorrent's XMLRPC",
    "rtorrent_password": "correct horse battery staple",

    "_ssh_username": "The ssh username to use for getting finished torrents from the remote host",
    "ssh_username": "JohnDoe",

    "_SSH_AUTH_COMMENT": "You may choose to use an ssh key or ssh password. If both are supplied, the password will not be used.",

    "_ssh_password": "The SSH password to use for SSH authentication",
    "ssh_password": "correct horse battery staple SSH",

    "_ssh_privkey_path": "The path to the private SSH key for SSH authentication",
    "ssh_privkey_path": "/home/tblyler/.ssh/id_rsa",

    "_ssh_addr": "The SSH address to connect to",
    "ssh_addr": "mysshserver.com:22",

    "_ssh_connect_timeout": "The time in nano seconds to wait for an SSH connection attempt",
    "ssh_connect_timeout": 30000000000,

    "_file_download_filemode": "The base 10 file mode to use for downloaded files",
    "file_download_filemode": 511,

    "_watch_to_download_paths": "The correlation of .torrent file paths and where their contents should be downloaded to",
    "watch_to_download_paths": {
        "/home/tblyler/torrent_files/tv": "/home/tblyler/Downloads/tv",
        "/home/tblyler/torrent_files/movies": "/home/tblyler/Downloads/movies",
        "/home/tblyler/torrent_files": "/home/tblyler/Downloads"
    },

    "_temp_download_path": "The root path to temporarily download to and then move to the folder in the setting above. The destination path is created underneath the temp_download_path",
    "temp_download_path": "/home/tblyler/tempDownloads",

    "_watch_to_finish_path": "If defined, the finished .torrent files finished are moved to their respected path here. Otherwise, they are deleted.",
    "watch_to_finish_path": {
        "/home/tblyler/torrent_files/tv": "/home/tblyler/completed_torrent_files/tv",
        "/home/tblyler/torrent_files": "/home/tblyler/completed_torrent_files"
    },

    "_rtorrent_update_interval": "The time in nano seconds to update the list of torrents and their statuses in rTorrent",
    "rtorrent_update_interval": 300000000000,

    "_download_jobs": "The number of concurrent download streams to have at one time",
    "download_jobs": 2
}
```
