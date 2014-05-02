import bencode
import hashlib
import json
import logging
import os
import pipes
import subprocess
import time
import xmlrpclib

"""
Interfaces with an rtorrent XML RPC server
"""
class rtorrentRPC:
	"""
	Constructor

	protocol string used to define the transport protocol for XMLRPC (http, https, etc)
	user     string used to define the username for XMLRPC
	password string used to define the password for XMLRPC
	host     string used to define the host for XMLRPC
	port     string/int used to define the port to use for XMLRPC
	path     string used to define the path for XMLRPC
	"""
	def __init__(self, protocol, user, password, host, port, path):
		# generates the string used for connection
		self.connectString = "%s://%s:%s@%s:%s/%s" % (str(protocol), str(user), str(password), str(host), str(port), str(path))

		# try to connect to the XMLRPC server
		if not self.connect():
			logging.warning('Indefinitely retrying to reconnect to "%s"' % self.connectString)
			while True:
				self.reconnect()

	"""
	Determines if we are connected to the XMLRPC server

	return bool true if we are connected, false if not
	"""
	def connected(self):
		try:
			self.connection.system.client_version()
			return True
		except:
			return False

	"""
	Try to conect to a server with multiple attempts

	attempts int the amount of times to attempt to connect to the XMLRPC server
	wait int the amount of seconds to wait between connection attempts

	return bool true if we connected, false if not
	"""
	def reconnect(self, attempts=5, wait=5):
		i = 0
		while i < attempts:
			logging.warning('Attempting to connect to "%s" attempt %d/%d' % (self.connectString, i + 1, attempts))
			if self.connect():
				return True

			time.sleep(wait)
			i += 1

		return False

	"""
	Connect to the XMLRPC server

	return bool true if we connected, false if not
	"""
	def connect(self):
		try:
			self.connection = xmlrpclib.ServerProxy(self.connectString)
			logging.info('Successfully connected to "%s"' % self.connectString)

			return self.connected()
		except:
			logging.warning('Failed to connect to "%s"' % self.connectString)
			return False

	"""
	Determines if a given torrent hash is completed or not

	hash string torrent hash to check on
	return bool true if it is complete, false if not, null if we have a connection issue
	"""
	def torrentComplete(self, hash):
		try:
			return self.connection.d.get_complete(hash) == 1
		except xmlrpclib.Fault:
			logging.warning('"%s" hash does not exist' % hash)
			return False
		except:
			logging.warning('Lost connection to "%s"' % self.connectString)
			# attempt to reconnect and run the query again
			if self.reconnect():
				return self.torrentComplete(hash)
			return None

	"""
	Gets the torrent name for the given torrent hash

	hash string torrent hash to check on
	return string the name of the torrent hash, false if it does not exist, null if we have a connection issue
	"""
	def torrentName(self, hash):
		try:
			return self.connection.d.get_name(hash)
		except xmlrpclib.Fault:
			logging.warning('"%s" hash does not exist' % hash)
			return False
		except:
			logging.warning('Lost connection to "%s"' % self.connectString)
			# attempt to reconnect and run the query again
			if self.reconnect():
				return self.torrentName(hash)
			return None

"""
Scans a directory for torrents and checks their completion status on the XMLRPC server
"""
class fileScan(rtorrentRPC):
	"""
	Constructor

	baseDir the location of the torrent files
	protocol string used to define the transport protocol for XMLRPC (http, https, etc)
	user     string used to define the username for XMLRPC
	password string used to define the password for XMLRPC
	host     string used to define the host for XMLRPC
	port     string/int used to define the port to use for XMLRPC
	path     string used to define the path for XMLRPC
	"""
	def __init__(self, baseDir, protocol, user, password, host, port, path):
		# call our parent's constructor
		rtorrentRPC.__init__(self, protocol, user, password, host, port, path)

		# make sure we have a trailing slash
		self.baseDir = baseDir
		if self.baseDir[-1] != '/':
			self.baseDir += '/'

	"""
	Gets a list of completed torrents from the self.baseDir path.

	return list of completed torrents [{'file': path, 'name': torrent_name}], None on connection issues
	"""
	def getCompleted(self):
		completed = []

		try:
			logging.info('Scanning for torrent files')
			for root, dirs, files in os.walk(self.baseDir, followlinks=True):
				for file in files:
					path = root + file
					hash = self.hashInfo(path)

					if not hash:
						logging.info('Skipping "%s", not a torrent file' % path)
					else:
						logging.debug('Hash for "%s" is "%s"' % (path, hash))
						isCompleted = self.torrentComplete(hash)
						if isCompleted == None:
							return None

						if isCompleted == True:
							name = self.torrentName(hash)
							if name == None:
								return None

							if name != False:
								logging.info('"%s" is completed, adding to download list' % name)
								completed.append({'file': path, 'name': name})
		except:
			return None

		return completed

	"""
	Get the torrent hash for a given filePath

	filePath string the path to a torrent file to hash
	return string the torrent hash of the file, false if there is an error parsing
	"""
	@staticmethod
	def hashInfo(filePath):
		try:
			return hashlib.sha1(bencode.bencode(bencode.bdecode(open(filePath, 'rb').read())['info'])).hexdigest().upper()
		except:
			return False

"""
Used for pushing torrent files to the server and pulling the actual data
"""
class puller:
	"""
	Constructor

	files list output from fileScan.getCompleted()
	username string the username used to connect to the remote server
	host string the location for the remote server
	port string/int the port used to connect to the remote server
	baseDir string the path the data is stored at remotely
	localDir string the path to download the data to
	torrentDir string the path where the local torrent files are located
	watchDir string the path where the remote server stores torrent files
	children int the amount of children to use for downloading
	"""
	def __init__(self, files, username, host, port, baseDir, localDir, torrentDir, watchDir, children=2):
		# the last time we pushed torrent files to the server
		self.lastPush = -1
		self.files = files
		self.processes = []
		self.children = children
		self.username = str(username)
		self.host = str(host)
		self.port = str(port)
		self.baseDir = str(baseDir)
		# make sure all directories have a trailing slash
		if self.baseDir[-1] != '/':
			self.baseDir += '/'
		self.localDir = str(localDir)
		if self.localDir[-1] != '/':
			self.localDir += '/'
		self.torrentDir = str(torrentDir)
		if self.torrentDir[-1] != '/':
			self.torrentDir += '/'
		self.watchDir = watchDir
		if self.watchDir[-1] != '/':
			self.watchDir += '/'

	"""
	Get the amount of torrents still in the queue for downloading

	return int the number of torrents waiting to be downloaded
	"""
	def queuedItems(self):
		return len(self.files)

	"""
	Get the number of torrents being downloaded right now

	return int the number of torrents being downloaded now
	"""
	def runningItems(self):
		return len(self.processes)

	"""
	Gets the amount of children that are free to download

	return int the number of children available to download
	"""
	def freeChildren(self):
		return self.children - self.runningItems()

	"""
	Spawns download children if allowed

	return bool true on download start, false on failure
	"""
	def pull(self):
		if self.queuedItems() == 0 or self.freeChildren() <= 0:
			return False
		file = self.files.pop(0)
		command = 'rsync --inplace --partial --port=%s -rq %s@%s:%s %s' % (self.port, self.username, self.host, pipes.quote(pipes.quote(self.baseDir + file['name'])), pipes.quote(self.localDir + '.temp/'))
		logging.info('Starting download for "%s"' % file['name'])
		logging.debug('command: "%s"' % command)

		try:
			process = subprocess.Popen(command, shell=True)
			self.processes.append({'process': process, 'info': file})
			return True
		except Exception as e:
			logging.error('Failed to start download for "%s" with error "%s"' % (file['name'], e))
			return False

	"""
	Pushes torrent files to the remote server

	return bool true on success, false on failure, none on error
	"""
	def push(self):
		now = time.time()
		# only push in 5 minute intervals
		if now - self.lastPush < 300:
			return True
		else:
			self.lastPush = now

		command = 'rsync --inplace --partial --port=%s -hrq %s %s@%s:%s' % (self.port, pipes.quote(self.torrentDir), self.username, self.host, pipes.quote(pipes.quote(self.watchDir)))
		logging.info('Syncing torrent files')
		logging.debug('command: "%s"' % command)

		try:
			if subprocess.call(command, shell=True) == 0:
				logging.info('Successfully synced torrent files')
				return True
			else:
				logging.warning('Failed to sync torrent files')
				return False
		except Exception as e:
			logging.error('Failed to sync torrents with error "%s"' % e)
			return None

	"""
	Cleanup processes that have finished

	return bool true if all children are finished, false otherwise
	"""
	def clean(self):
		i = 0
		length = len(self.processes)

		while i < length:
			if self.processes[i]['process'].poll() != None:
				if self.processes[i]['process'].returncode == 0:
					logging.info('Successfully downloaded "%s"' % self.processes[i]['info']['name'])
					os.renames(self.localDir + '.temp/' + self.processes[i]['info']['name'], self.localDir + self.processes[i]['info']['name'])
					os.unlink(self.processes[i]['info']['file'])
				else:
					logging.warning('Failed to download "%s"' % self.processes[i]['info']['name'])
				self.processes.pop(i)
				i -= 1
				length -= 1
			i += 1

		return length == 0

"""
Starts up fileScan and puller instances through given config file
"""
class loader:
	"""
	Constructor

	configFile string the location for the config file
	"""
	def __init__(self, configFile):
		self.configFile = configFile
		self.data = None

	"""
	Loads the configuration file into memory and parses it as JSON

	return bool true on success, false otherwise
	"""
	def loadFile(self):
		try:
			data = open(self.configFile, 'rb').read()
			self.data = json.loads(data)
			logging.info('Successfully loaded config from "%s"' % self.configFile)
			return True
		except:
			logging.error('Failed to load configuration from "%s"' % self.configFile)
			return False

	"""
	Initialize a fileScanner and puller class based off the configuration file

	return {'puller': puller(), 'scanner': fileScanner()}, False on error
	"""
	def load(self):
		if self.data == None and not self.loadFile():
			return False

		try:
			fileScanner = fileScan(baseDir=self.data['torrent_files'], protocol=self.data['xmlrpc']['transport'], user=self.data['xmlrpc']['user'], password=self.data['xmlrpc']['password'], host=self.data['xmlrpc']['host'], port=self.data['xmlrpc']['port'], path=self.data['xmlrpc']['path'])

			completed = fileScanner.getCompleted()
			if completed == None:
				logging.warning('Nothing to parse due to connection issues')
				return False
			else:
				pull = puller(files=completed, username=self.data['torrent_download']['user'], host=self.data['torrent_download']['host'], port=self.data['torrent_download']['port'], baseDir=self.data['torrent_download']['download_dir'], localDir=self.data['local_download_dir'], torrentDir=self.data['torrent_files'], watchDir=self.data['torrent_download']['watch_dir'])

			return {'puller': pull, 'scanner': fileScanner}
		except:
			return False

if __name__ == '__main__':
	import argparse

	logging.basicConfig(level=logging.INFO, format='[%(asctime)s] %(message)s')
	logger = logging.getLogger(__name__)

	parser = argparse.ArgumentParser(description='Download torrents that are completed')
	parser.add_argument( '-c', '--config', dest='config_file', metavar='CONFIG_FILE', nargs=1, help='Location of config file formatted in JSON. Default: ./hoarder.conf')

	args = parser.parse_args()

	if args.config_file == None:
		config_file = './hoarder.conf'
	else:
		config_file = args.config_file

		if not os.path.isfile(config_file):
			logging.error('Cannot find config file "%s"' % config_file)
			exit(1)

	load = loader(config_file)
	while True:
		parsing = load.load()
		lastParse = time.time()

		if not parsing:
			logging.error('Failed to start the parse, fetch and upload process')
			exit(1)

		while True:
			parsing['puller'].push()
			parsing['puller'].pull()
			if parsing['puller'].clean():
				break

			if parsing['puller'].queuedItems() == 0 and parsing['puller'].freeChildren() > 0 and time.time() - lastParse > 600:
				for item in parsing['scanner'].getCompleted():
					running = False
					for runningItem in parsing['puller'].processes:
						if item == runningItem['info']:
							running = True
							break
					if not running:
						parsing['puller'].files.append(item)

			time.sleep(1)

		time.sleep(300)
