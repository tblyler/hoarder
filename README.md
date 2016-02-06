Hoarder
========
Uploads .torrent files from a local "blackhole" to a remote (SSH) rtorrent watch folder. From there, rtorrent is polled over XMLRPC as to whether the torrent is completed. Finally, the files are downloaded over a multithreaded SSH connection and saved to the local machine. The blackhole is used as a queue and will have its .torrent files deleted.

Requirements
------------
* bash >= 4.0 (support for associative arrays)
* python2
* curl
* rsync
* scp

Configuration
-------------
Edit the variables at the top of hoarder.sh to your liking.

Running
-------
Run hoarder.sh with bash
