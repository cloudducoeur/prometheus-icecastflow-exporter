# prometheus-icecastflow-exporter

This exporter allows you to verify, with `ffmpeg`, that the audio stream is properly listening (live).

## Compilation and installation on Debian 12

### Prerequisites

```bash
# Install Go and ffmpeg
sudo apt update
sudo apt install golang-go ffmpeg git

# Create a system user for the service
sudo useradd --system --shell /bin/false --home-dir /var/lib/prometheus --create-home prometheus
```

### Compilation

```bash
# Clone the project
git clone https://github.com/cloudducoeur/prometheus-icecastflow-exporter
cd prometheus-icecastflow-exporter

# Build the binary
go build -o prometheus-icecastflow-exporter main.go
```

### Installation

```bash
# Copy the binary
sudo cp prometheus-icecastflow-exporter /usr/local/bin/
sudo chmod +x /usr/local/bin/prometheus-icecastflow-exporter

# Create the configuration directory
sudo mkdir -p /etc/prometheus-icecastflow-exporter
sudo cp config.yml /etc/prometheus-icecastflow-exporter/
sudo chown -R prometheus:prometheus /etc/prometheus-icecastflow-exporter

# Install the systemd service
sudo cp prometheus-icecastflow-exporter.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable prometheus-icecastflow-exporter
sudo systemctl start prometheus-icecastflow-exporter
```

### Verification

```bash
# Check the service status
sudo systemctl status prometheus-icecastflow-exporter

# View the logs
sudo journalctl -u prometheus-icecastflow-exporter -f

# Test the Prometheus endpoint
curl http://localhost:2112/metrics
```

## Usage

### Available options

```bash
./prometheus-icecastflow-exporter --help
  -config string
        Path to the configuration file (default "config.yml")
  -listen string
        Address and port to listen on (default ":2112")
```

### Usage examples

```bash
# Use with default values
./prometheus-icecastflow-exporter

# Specify a custom config file
./prometheus-icecastflow-exporter --config /path/to/my/config.yml

# Specify a custom listening address
./prometheus-icecastflow-exporter --listen :8080

# Use both options
./prometheus-icecastflow-exporter --config /etc/prometheus-icecastflow-exporter/config.yml --listen 0.0.0.0:9090
```

### Example output

```text
2025/07/07 14:26:37 2 streams loaded from config.yml
2025/07/07 14:26:37 Audio stream exporter running on :2112/metrics
2025/07/07 14:26:38 Stream OK: https://ice.creacast.com/radio-restos
2025/07/07 14:26:39 Stream OK: https://radiorestos.ice.infomaniak.ch/radiorestos-192.aac
```

## Prometheus Configuration

Add this configuration to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'icecastflow-exporter'
    static_configs:
      - targets: ['localhost:2112']
    scrape_interval: 30s
```

## Exposed Metrics

- `audio_stream_up{url="..."}`: Indicates if the audio stream is online (1) or offline (0)
