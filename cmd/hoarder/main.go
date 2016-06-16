package main

import (
	"flag"
	"fmt"
	"github.com/go-yaml/yaml"
	"github.com/tblyler/hoarder/queue"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strconv"
	"time"
)

var buildVersion = "Unknown"
var buildDate = "Unknown"

func main() {
	version := flag.Bool("version", false, "display version info")
	configPath := flag.String("config", "", "path to the config file")
	flag.Parse()

	if *version {
		dateUnix, err := strconv.ParseInt(buildDate, 10, 64)
		if err == nil {
			date := time.Unix(dateUnix, 0)
			if !date.IsZero() {
				buildDate = date.UTC().Format(time.UnixDate)
			}
		}

		fmt.Printf("%s\n%s\n", buildVersion, buildDate)
		os.Exit(0)
	}

	logger := log.New(os.Stdout, "hoarder ", log.LstdFlags)

	if *configPath == "" {
		logger.Println("Missing config path")
		os.Exit(1)
	}

	configRaw, err := ioutil.ReadFile(*configPath)
	if err != nil {
		logger.Printf("Failed to read config file '%s': '%s'", *configPath, err)
		os.Exit(1)
	}

	config := &queue.Config{}
	err = yaml.Unmarshal(configRaw, config)
	if err != nil {
		logger.Printf("Unable to decode config json at '%s': '%s'", *configPath, err)
		os.Exit(1)
	}

	q, err := queue.NewQueue(config, logger)
	if err != nil {
		logger.Printf("Failed to start hoarder: '%s'", err)
		os.Exit(1)
	}

	stop := make(chan bool)
	done := make(chan bool)
	go func() {
		q.Run(stop)
		done <- true
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	sig := <-sigChan
	logger.Println("Got signal ", sig, " quitting")
	stop <- true
	<-done
}
