#!/bin/sh

## START CONFIGURATION
# Location where the .torrent files are stored locally
TORRENT_FILE_PATH='/home/tblyler/torrent_files'
# Location to initially download torrent data to from the remote SSH server
TORRENT_TMP_DOWNLOAD='/home/tblyler/torrents_tmp'
# Location to move the completed torrent data to from TORRENT_TMP_DOWNLOAD
TORRENT_DOWNLOAD='/home/tblyler/torrents'
# Amount of rsync processes to have running at one time
RSYNC_PROCESSES=2
# Location on the remote SSH server to copy the .torrent files to
SSH_SERVER_TORRENT_FILE_PATH='watch'
# Location on the remote SSH server where the torrent data is stored
SSH_SERVER_DOWNLOAD_PATH='files'
# Address of the remote SSH server where the torrents are downloaded
SSH_SERVER='remote.rtorrent.com'
# The username to use to login to the SSH server
SSH_USER='sshUserName'
# The XMLRPC basic HTTP authentication username
XML_USER='XMLRPCUserName'
# The XMLRPC basic HTTP authentication password
XML_PASS='XMLRPCPassword'
# The XMLRPC url
XML_URL='https://XMLRPCURL.com/XMLRPC'
## END CONFIGURATION

if ! which curl > /dev/null; then
	echo 'curl must be installed'
	exit 1
fi

if ! which scp > /dev/null; then
	echo 'scp must be installed'
	exit 1
fi

if ! which rsync > /dev/null; then
	echo 'rsync must be installed'
	exit 1
fi

if ! which python > /dev/null; then
	if ! which python2 > /dev/null; then
		echo 'python must be install'
		exit 1
	fi
fi

# Hacky method to create the XML for an XMLRPC request to rtorrent
xml() {
	local method=$1
	local args=$2
	echo "<?xml version='1.0'?>
<methodCall>
<methodName>${method}</methodName>
<params>
<param>
<value><string>${args}</string></value>
</param>
</params>
</methodCall>"
}

# Returns the current entity and its content in an XML response
read_dom() {
	local IFS=\>
	read -d \< ENTITY CONTENT
}

# Sends an XMLRPC request to rtorrent via curl and returns its data
xml_curl() {
	local method=$1
	local args=$2
	local xml_post=`xml "${method}" "${args}"`
	local curl_command='curl -s'
	if [[ "${XML_USER}" != '' ]]; then
		local curl_command="${curl_command} --basic -u '${XML_USER}"
		if [[ "${XML_USER}" != '' ]]; then
			local curl_command="${curl_command}:${XML_PASS}"
		fi

		local curl_command="${curl_command}'"
	fi

	local curl_command="${curl_command} -d \"${xml_post}\" '${XML_URL}'"

	local xml_response=$(eval "${curl_command}")
	local curl_return=$?

	echo "${xml_response}"
	return $curl_return
}

# Gets .torrent's name from the remote rtorrent XMLRPC
get_torrent_name() {
	local torrent_hash=$1
	local xml_response=`xml_curl d.get_name "${torrent_hash}"`
	local curl_return=$?

	if [[ "${curl_return}" -ne 0 ]]; then
		echo "Curl failed to get torrent name with error code ${curl_return}"
		return $curl_return
	fi

	local torrent_name=`echo "${xml_response}" | while read_dom; do
		if [[ "${ENTITY}" = "name" ]] && [[ "${CONTENT}" = "faultCode" ]]; then
			local error=true
		fi

		if [[ ! "${error}" ]] && [[ "${ENTITY}" = "string" ]]; then
			echo "${CONTENT}"
		fi
	done`

	if [[ "${torrent_name}" = '' ]]; then
		echo "${xml_response}"
		return 1
	else
		echo "${torrent_name}"
		return 0
	fi
}

# Get .torrent's completion status from the remote rtorrent
get_torrent_complete() {
	local torrent_hash=$1
	local xml_response=`xml_curl d.get_complete "${torrent_hash}"`
	local curl_return=$?

	if [[ "${curl_return}" -ne 0 ]]; then
		echo "Curl failed to get torrent name with error code ${curl_return}"
		return ${curl_return}
	fi

	local torrent_completed=`echo "${xml_response}" | while read_dom; do
		if [[ "${ENTITY}" = "name" ]] && [[ "${CONTENT}" = "faultCode" ]]; then
			local error=true
		fi

		if [[ ! "${error}" ]] && [[ "${ENTITY}" = "i8" ]]; then
			echo "${CONTENT}"
		fi
	done`

	if [[ "${torrent_completed}" = '' ]]; then
		echo "${xml_response}"
		return 1
	else
		echo "${torrent_completed}"
		return 0
	fi
}

# Check if a .torrent is loaded on the remote rtorrent
get_torrent_added() {
	local torrent_hash=$1
	local xml_response=`xml_curl d.get_complete "${torrent_hash}"`
	local curl_return=$?

	if [[ "${curl_return}" -ne 0 ]]; then
		echo "Curl failed to get torrent name with error code ${curl_return}"
		return ${curl_return}
	fi

	local torrent_added=`echo "${xml_response}" | while read_dom; do
		if [[ "${CONTENT}" = 'Could not find info-hash.' ]]; then
			echo "${CONTENT}"
		fi
	done`

	if [[ "${torrent_added}" = '' ]]; then
		echo 1
	else
		echo 0
	fi
}

# Get the info hash for a given .torrent file
get_torrent_hash() {
	local torrent_file=$1
	if [[ ! -f "${torrent_file}" ]]; then
		return 1
	fi

	local python_bin='python2'
	if ! which "${python_bin}" 2>&1 > /dev/null; then
		local python_bin='python'
		if ! which "${python_bin}" 2>&1 > /dev/null; then
			return 1
		fi
	fi

	local torrent_hash=`"${python_bin}" - << END
import hashlib

def compute_hash(file_path):
    try:
        data = open(file_path, 'rb').read()
    except:
        return False
    data_len = len(data)
    start = data.find("infod")
    if start == -1:
        return False

    start += 4
    current = start + 1
    dir_depth = 1
    while current < data_len and dir_depth > 0:
        if data[current] == 'e':
            dir_depth -= 1
            current += 1
        elif data[current] == 'l' or data[current] == 'd':
            dir_depth += 1
            current += 1
        elif data[current] == 'i':
            current += 1
            while data[current] != 'e':
                current += 1
            current += 1
        elif data[current].isdigit():
            num = data[current]
            current += 1
            while data[current] != ':':
                num += data[current]
                current += 1
            current += 1 + int(num)
        else:
            return False

    return hashlib.sha1(data[start:current]).hexdigest().upper()

print(compute_hash("${torrent_file}"))
END
	`

	if [[ ! $? ]] || [[ "${torrent_hash}" = 'False' ]]; then
		return 1
	fi

	echo $torrent_hash
}

# keep track of the .torrent files to be downloaded
declare -A TORRENT_QUEUE
# keep track of the rsyncs to download torrent data
declare -A RUNNING_RSYNCS
# run indefinitely
while true; do
	# check to make sure the path of the local .torrent files exists
	if [[ ! -d "${TORRENT_FILE_PATH}" ]]; then
		echo "${TORRENT_FILE_PATH} Does not exist"
		exit 1
	fi
	
	OIFS="$IFS"
	IFS=$'\n'
	# enumerate the .torrent file directory
	for file in `find "${TORRENT_FILE_PATH}"`; do
		# check if the path is a directory
		if [[ -d "${file}" ]]; then
			# enumerate the directory
			for sub_file in `find "${file}" -type f`; do
				# this is the furthest we will descend
				if [[ -f "${sub_file}" ]]; then
					# get the torrent hash for the .torrent file
					torrent_hash=`get_torrent_hash "${sub_file}"`
					if [[ ! $? ]]; then
						echo "Failed to get the torrent hash of ${sub_file}"
						continue
					fi

					# add the torrent to the queue if it is not already in the queue
					if [[ ! ${TORRENT_QUEUE[${torrent_hash}]+_} ]]; then
						TORRENT_QUEUE[$torrent_hash]="${sub_file}"
					fi
				fi
			done
		# check that the path is a file
		elif [[ -f "${file}" ]]; then
			# get the torrent hash for the .torrent file
			torrent_hash=`get_torrent_hash "${file}"`
			if [[ ! $? ]]; then
				echo "Failed to get the torrent hash of ${file}"
				continue
			fi

			# add the torrent to the queue if it is not already in the queue
			if [[ ! ${TORRENT_QUEUE[${torrent_hash}]+_} ]]; then
				TORRENT_QUEUE[$torrent_hash]="${file}"
			fi
		fi
	done
	IFS="$OIFS"

	# go through the torrent queue
	for torrent_hash in "${!TORRENT_QUEUE[@]}"; do
		# continue if the torrent is already being downloaded
		if [[ ${RUNNING_RSYNCS[$torrent_hash]+_} ]]; then
			continue
		fi

		# check to see if the torrent is on the rtorrent server
		torrent_added=`get_torrent_added "${torrent_hash}"`
		if [[ ! $? ]]; then
			echo "Failed to see if ${TORRENT_QUEUE[$torrent_hash]} exists on the rtorrent server"
			continue
		fi

		# if the torrent is not on the rtorrent server, upload it
		if [[ $torrent_added -eq 0 ]]; then
			scp "${TORRENT_QUEUE[$torrent_hash]}" "${SSH_USER}@${SSH_SERVER}:${SSH_SERVER_TORRENT_FILE_PATH}"
			if [[ ! $? ]]; then
				echo "Failed to upload ${TORRENT_QUEUE[$torrent_hash]}"
			fi
		fi
	done

	# if the amount of running rsyncs is below the desire amount, run items from the queue
	for torrent_hash in "${!TORRENT_QUEUE[@]}"; do
		# break out of the loop if we added enough jobs already
		if [[ ${#RUNNING_RSYNCS[@]} -ge ${RSYNC_PROCESSES} ]]; then
			break
		fi
		# make sure this torrent is not already being downloaded
		if [[ ${RUNNING_RSYNCS[${torrent_hash}]+_} ]]; then
			continue
		fi

		# see if the torrent is finished downloading remotely
		torrent_completed=`get_torrent_complete "${torrent_hash}"`
		if [[ ! $? ]]; then
			echo "Failed to check if ${TORRENT_QUEUE[$torrent_hash]} is completed"
			continue
		fi

		# the torrent is finished downloading remotely
		if [[ "${torrent_completed}" -eq 1 ]]; then
			torrent_name=`get_torrent_name "${torrent_hash}"`
			if [[ ! $? ]]; then
				echo "Failed to get torrent name for ${TORRENT_QUEUE[$torrent_hash]}"
				continue
			fi

			# start the download and record the PID
			rsync -hrvP --inplace "${SSH_USER}@${SSH_SERVER}:\"${SSH_SERVER_DOWNLOAD_PATH}/${torrent_name}"\" "${TORRENT_TMP_DOWNLOAD}/" > /dev/null &
			RUNNING_RSYNCS[${torrent_hash}]=$!
		fi
	done

	# checkup on the running rsyncs
	for torrent_hash in "${!RUNNING_RSYNCS[@]}"; do
		pid=${RUNNING_RSYNCS[$torrent_hash]}
		# check to see if the given PID is still running
		if ! kill -0 "${pid}" 2> /dev/null; then
			# get the return code of the PID
			wait $pid
			return=$?
			if [[ $return ]]; then
				echo "Successfully downloaded ${TORRENT_QUEUE[$torrent_hash]}"
				torrent_name=`get_torrent_name "${torrent_hash}"`
				if [[ $? ]]; then
					final_location_dir="${TORRENT_DOWNLOAD}"
					if [[ `dirname "${TORRENT_QUEUE[$torrent_hash]}"` != "${TORRENT_FILE_PATH}" ]]; then
						final_location_dir="${final_location_dir}/$(basename "`dirname "${TORRENT_QUEUE[$torrent_hash]}"`")"
					fi

					if [[ ! -d "${final_location_dir}" ]]; then
						mkdir -p "${final_location_dir}"
					fi

					mv "${TORRENT_TMP_DOWNLOAD}/${torrent_name}" "${final_location_dir}/"
					rm "${TORRENT_QUEUE[$torrent_hash]}"
					unset TORRENT_QUEUE[$torrent_hash]
				else
					echo "Failed to get torrent name for ${TORRENT_QUEUE[$torrent_hash]}"
				fi
			else
				echo "Failed to download ${TORRENT_QUEUE[$torrent_hash]} with rsync return code $return"
			fi

			unset RUNNING_RSYNCS[$torrent_hash]
		fi
	done

	sleep 5s
done
