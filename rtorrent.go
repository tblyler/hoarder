// Communicates with rtorrent's XMLRPC interface, and can gather info regarding a .torrent file.
package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"
	bencode "github.com/jackpal/bencode-go"
)

// Keeps track of a torrent file's and rtorrent's XMLRPC information.
type Torrent struct {
	path string
	hash string
	xml_user string
	xml_pass string
	xml_address string
}

// Create a new Torrent instance while computing its hash.
func NewTorrent(xml_user string, xml_pass string, xml_address string, file_path string) (*Torrent, error) {
	hash, err := getTorrentHash(file_path)
	if err != nil {
		return nil, err
	}

	return &Torrent{file_path, hash, xml_user, xml_pass, xml_address}, nil
}

// Compute the torrent hash for a given torrent file path returning an all caps sha1 hash as a string.
func getTorrentHash(file_path string) (string, error) {
	file, err := os.Open(file_path)
	if err != nil {
		return "", err
	}

	defer file.Close()

	data, err := bencode.Decode(file)
	if err != nil {
		return "", err
	}

	decoded, ok := data.(map[string]interface{})
	if !ok {
		return "", errors.New("unable to convert data to map")
	}

	var encoded bytes.Buffer
	bencode.Marshal(&encoded, decoded["info"])

	encoded_string := encoded.String()

	hash := sha1.New()
	io.WriteString(hash, encoded_string)

	hash_string := strings.ToUpper(hex.EncodeToString(hash.Sum(nil)))

	return hash_string, nil
}

// Send a command and its argument to the rtorrent XMLRPC and get the response.
func (t Torrent) xmlRpcSend (command string, arg string) (string, error) {
	// This is hacky XML to send to the server
	buf := []byte("<?xml version='1.0'?>\n" +
	"<methodCall>\n" +
	"<methodName>" + command + "</methodName>\n" +
	"<params>\n" +
	"<param>\n" +
	"<value><string>" + arg + "</string></value>\n" +
	"</param>\n" +
	"</params>\n" +
	"</methodCall>\n")

	buffer := bytes.NewBuffer(buf)

	request, err := http.NewRequest("POST", t.xml_address, buffer)
	if err != nil {
		return "", err
	}

	// Set the basic HTTP auth if we have a user or password
	if t.xml_user != "" || t.xml_pass != "" {
		request.SetBasicAuth(t.xml_user, t.xml_pass)
	}

	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	re, err := regexp.Compile("<value><.*>(.*)</.*></value>")
	if err != nil {
		return "", err
	}

	values := re.FindAllStringSubmatch(string(body), -1)

	if len(values) != 1 {
		return "", nil
	}

	return values[0][1], nil
}

// Get the torrent's name from rtorrent.
func (t Torrent) GetTorrentName() (string, error) {
	return t.xmlRpcSend("d.get_name", t.hash)
}

// Get the completion status of the torrent from rtorrent.
func (t Torrent) GetTorrentComplete() (bool, error) {
	complete, err := t.xmlRpcSend("d.get_complete", t.hash)
	if err != nil {
		return false, err
	}

	return complete == "1", nil
}
