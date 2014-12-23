// Configuration for Hoarder
// All fields and values are necessary

// Username for XMLRPC
xml_user: "testuser"
// Password for XMLRPC
xml_pass: "supersecure"
// Address to XMLRPC
xml_address: "https://mysweetaddress:443/xmlrpc"
// Amount of download threads per file
threads: "4"
// Username for logging into the ssh server
ssh_user: "testsshuser"
// Password for the logging into the ssh server
ssh_pass: "bestpasswordever"
// Server address for the ssh server
ssh_server: "sshserveraddress"
// Port for the ssh server
ssh_port: "22"
// Location to temporarily download files
temp_download_dir: "/home/user/tmp_download/"
// Location to move downloaded files from temp_download_dir to
local_download_dir: "/home/user/Downloads/"
// Locaiton to watch for .torrent files
local_torrent_dir: "/home/user/torrent_files/"
// Remote location on the SSH server to download torrent data from
remote_download_dir: "/home/user/files/"
// Remote location on the SSH server to upload .torrent files to
remote_torrent_dir: "/home/user/watch/"
