Hoarder
========
Uploads .torrent files from a local "blackhole" to a remote (SSH) rtorrent watch folder. From there, rtorrent is polled over XMLRPC as to whether the torrent is completed. Finally, the files are downloaded over a multithreaded SSH connection and saved to the local machine. The blackhole is used as a queue and will have its .torrent files deleted.

Requirements
------------
Go

Install
-------
1. `go get github.com/tblyler/hoarder`
2. `go install github.com/tblyler/hoarder`

Configuration
-------------
Make sure you make a copy of the conf file in the repo to suit your needs.

Running
-------
After installation, just run the hoarder executable with the --config flag to specify where the config file is.
