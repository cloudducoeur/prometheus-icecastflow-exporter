package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	Streams []string `yaml:"streams"`
}

var (
	audioStreamUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "audio_stream_up",
			Help: "Indicates if the audio stream is online",
		},
		[]string{"url"},
	)

	config Config
)

func loadConfig(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Config read error: %v", err)
	}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("YAML parsing error: %v", err)
	}
	log.Printf("%d streams loaded from %s", len(config.Streams), path)
}

func checkStream(url string) {
	cmd := exec.Command("ffmpeg", "-v", "error", "-t", "2", "-i", url, "-f", "null", "-")
	err := cmd.Run()
	if err != nil {
		log.Printf("Stream KO: %s (%v)", url, err)
		audioStreamUp.WithLabelValues(url).Set(0)
	} else {
		log.Printf("Stream OK: %s", url)
		audioStreamUp.WithLabelValues(url).Set(1)
	}
}

func probeAll() {
	for _, url := range config.Streams {
		go checkStream(url)
	}
}

func main() {
	var (
		configPath = flag.String("config", "config.yml", "Path to the configuration file")
		listenAddr = flag.String("listen", ":2112", "Address and port to listen on")
	)
	flag.Parse()

	loadConfig(*configPath)
	prometheus.MustRegister(audioStreamUp)

	go func() {
		for {
			probeAll()
			time.Sleep(30 * time.Second)
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	log.Printf("Audio stream exporter running on %s/metrics", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
