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
		Help: "Duration of the last silence in seconds",
	},
	[]string{"url"},
)

// Additional audio quality metrics
var loudnessRMS = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_loudness_rms",
		Help: "Average RMS level in dB",
	},
	[]string{"url"},
)

var peakLevel = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_peak_level",
		Help: "Peak level in dB",
	},
	[]string{"url"},
)

var clippedSamples = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "audio_clipped_samples_total",
		Help: "Total number of clipped samples",
	},
	[]string{"url"},
)

var dynamicRange = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "audio_dynamic_range",
		Help: "Dynamic range (dB)",
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

func monitorAudio(streamURL string, silenceMin float64, noise string) {
	// Use info log level to ensure astats output is visible.
	filter := fmt.Sprintf("silencedetect=noise=%s:d=%f,astats=metadata=1:reset=1", noise, silenceMin)
	reSilenceDur := regexp.MustCompile(`silence_duration: ([0-9.]+)`)
	// Match variants: "RMS level:" "RMS_level:" (optional dB after number) etc.
	reRMSHuman := regexp.MustCompile(`(?i)RMS[ _]level:? *(-?[0-9.]+)`)
	rePeakHuman := regexp.MustCompile(`(?i)Peak[ _]level:? *(-?[0-9.]+)`)
	reClipHuman := regexp.MustCompile(`(?i)Number of clipped samples: *(\d+)`)
	reDynHuman := regexp.MustCompile(`(?i)Dynamic range: *([0-9.]+)`)

	for {
		cmd := exec.Command("ffmpeg", "-hide_banner", "-v", "info", "-i", streamURL, "-af", filter, "-f", "null", "-")

		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("audio monitor pipe error for %s: %v", streamURL, err)
			time.Sleep(10 * time.Second)
			continue
		}
		if err := cmd.Start(); err != nil {
			log.Printf("audio monitor start error for %s: %v", streamURL, err)
			time.Sleep(10 * time.Second)
			continue
		}

		scanner := bufio.NewScanner(stderr)
		buf := make([]byte, 0, 128*1024)
		scanner.Buffer(buf, 512*1024) // increase buffer for long astats lines
		inSilence := false

		for scanner.Scan() {
			line := scanner.Text()

			// Silence detection
			if strings.Contains(line, "silence_start") {
				if !inSilence {
					inSilence = true
					silenceActive.WithLabelValues(streamURL).Set(1)
				}
				continue
			}
			if strings.Contains(line, "silence_end") {
				if m := reSilenceDur.FindStringSubmatch(line); len(m) == 2 {
					if dur, err := strconv.ParseFloat(m[1], 64); err == nil {
						silenceDuration.WithLabelValues(streamURL).Set(dur)
					}
				}
				inSilence = false
				silenceActive.WithLabelValues(streamURL).Set(0)
				continue
			}

			// Human-readable astats lines
			if m := reRMSHuman.FindStringSubmatch(line); len(m) == 2 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					loudnessRMS.WithLabelValues(streamURL).Set(v)
				}
			}
			if m := rePeakHuman.FindStringSubmatch(line); len(m) == 2 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					peakLevel.WithLabelValues(streamURL).Set(v)
				}
			}
			if m := reClipHuman.FindStringSubmatch(line); len(m) == 2 {
				if n, err := strconv.ParseFloat(m[1], 64); err == nil && n > 0 {
					clippedSamples.WithLabelValues(streamURL).Add(n)
				}
			}
			if m := reDynHuman.FindStringSubmatch(line); len(m) == 2 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					dynamicRange.WithLabelValues(streamURL).Set(v)
				}
			}

			// metadata=1 key=value variant (lavfi.astats.*)
			if strings.Contains(line, "lavfi.astats") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					key := parts[0]
					val := parts[1]
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						switch {
						case strings.HasSuffix(key, ".RMS_level"):
							loudnessRMS.WithLabelValues(streamURL).Set(f)
						case strings.HasSuffix(key, ".Peak_level"):
							peakLevel.WithLabelValues(streamURL).Set(f)
						case strings.HasSuffix(key, ".Number_of_clipped_samples") && f > 0:
							clippedSamples.WithLabelValues(streamURL).Add(f)
						case strings.HasSuffix(key, ".Dynamic_range"):
							dynamicRange.WithLabelValues(streamURL).Set(f)
						}
					}
				}
			}
		}

		if err := cmd.Wait(); err != nil {
			log.Printf("audio monitor ended for %s (will restart): %v", streamURL, err)
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
	prometheus.MustRegister(
		audioStreamUp,
		silenceActive,
		silenceDuration,
		loudnessRMS,
		peakLevel,
		clippedSamples,
		dynamicRange,
	)

	// Initialize silence metrics for all configured streams
	for _, url := range config.Streams {
		silenceActive.WithLabelValues(url).Set(0)
		silenceDuration.WithLabelValues(url).Set(0)
		loudnessRMS.WithLabelValues(url).Set(0)
		peakLevel.WithLabelValues(url).Set(0)
		dynamicRange.WithLabelValues(url).Set(0)
		// clippedSamples is a counter; starts at 0 implicitly
	}

	// Launch audio monitoring goroutines (silence + astats)
	for _, url := range config.Streams {
		go monitorAudio(url, config.SilenceMinSeconds, config.SilenceNoiseLevel)
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
