package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	Streams           []string `yaml:"streams"`
	SilenceMinSeconds float64  `yaml:"silence_min_seconds"` // minimum duration to consider a silence
	SilenceNoiseLevel string   `yaml:"silence_noise_level"` // e.g. -30dB
}

var audioStreamUp = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_stream_up",
		Help: "Indicates if the audio stream is online",
	},
	[]string{"url"},
)

var silenceActive = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_silence_active",
		Help: "1 if a silence >= configured duration is detected, 0 otherwise",
	},
	[]string{"url"},
)

var silenceDuration = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_silence_duration_seconds",
		Help: "Duration of the last detected silence (seconds)",
	},
	[]string{"url"},
)

var config Config

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
	// Defaults
	if config.SilenceMinSeconds <= 0 {
		config.SilenceMinSeconds = 5.0
	}
	if strings.TrimSpace(config.SilenceNoiseLevel) == "" {
		config.SilenceNoiseLevel = "-30dB"
	}
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

func monitorSilence(streamURL string, silenceMin float64, noise string) {
	for {
		cmd := exec.Command("ffmpeg", "-i", streamURL,
			"-af", fmt.Sprintf("silencedetect=noise=%s:d=%f", noise, silenceMin),
			"-f", "null", "-")

		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("silence monitor pipe error for %s: %v", streamURL, err)
			time.Sleep(10 * time.Second)
			continue
		}

		if err := cmd.Start(); err != nil {
			log.Printf("silence monitor start error for %s: %v", streamURL, err)
			time.Sleep(10 * time.Second)
			continue
		}

		scanner := bufio.NewScanner(stderr)
		re := regexp.MustCompile(`silence_duration: ([0-9.]+)`) // matches duration after silence_end
		// Ensure gauges exist
		silenceActive.WithLabelValues(streamURL).Set(0)
		silenceDuration.WithLabelValues(streamURL).Set(0)

		// track if currently in silence (avoid redundant sets)
		inSilence := false

		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "silence_start") {
				if !inSilence {
					inSilence = true
					silenceActive.WithLabelValues(streamURL).Set(1)
					log.Printf("Silence start detected on %s", streamURL)
				}
			} else if strings.Contains(line, "silence_end") {
				m := re.FindStringSubmatch(line)
				if len(m) == 2 {
					if dur, err := strconv.ParseFloat(m[1], 64); err == nil {
						silenceDuration.WithLabelValues(streamURL).Set(dur)
						log.Printf("Silence end on %s duration=%.2fs", streamURL, dur)
					}
				}
				inSilence = false
				silenceActive.WithLabelValues(streamURL).Set(0)
			}
		}

		// If ffmpeg exits or scanner breaks, wait and restart
		if err := cmd.Wait(); err != nil {
			log.Printf("silence monitor ended for %s (will restart): %v", streamURL, err)
		} else {
			log.Printf("silence monitor ended cleanly for %s (will restart)", streamURL)
		}
		time.Sleep(5 * time.Second)
	}
}

func main() {
	var (
		configPath = flag.String("config", "config.yml", "Path to the configuration file")
		listenAddr = flag.String("listen", ":2112", "Address and port to listen on")
	)
	flag.Parse()

	loadConfig(*configPath)
	prometheus.MustRegister(audioStreamUp, silenceActive, silenceDuration)

	// Initialize silence metrics for all configured streams
	for _, url := range config.Streams {
		silenceActive.WithLabelValues(url).Set(0)
		silenceDuration.WithLabelValues(url).Set(0)
	}

	// Launch silence monitoring goroutines
	for _, url := range config.Streams {
		go monitorSilence(url, config.SilenceMinSeconds, config.SilenceNoiseLevel)
	}

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
