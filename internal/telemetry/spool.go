package telemetry

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var spool = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "selfmail_postfix_spool", Help: "Postfix spool bytes, message count and filesystem free bytes"}, []string{"measurement"})
var spoolObserved = prometheus.NewGauge(prometheus.GaugeOpts{Name: "selfmail_postfix_spool_timestamp_seconds", Help: "Root Postfix spool sample time"})

func ReadSpool(logPath string) {
	f, e := os.Open(filepath.Join(filepath.Dir(logPath), ".spool.snapshot"))
	if e != nil {
		return
	}
	defer f.Close()
	reader := bufio.NewScanner(f)
	reader.Buffer(make([]byte, 1024), 1024)
	for reader.Scan() {
		parts := strings.SplitN(reader.Text(), "=", 2)
		if len(parts) != 2 {
			continue
		}
		n, e := strconv.ParseFloat(parts[1], 64)
		if e != nil {
			continue
		}
		switch parts[0] {
		case "observed_at":
			spoolObserved.Set(n)
		case "bytes", "messages", "disk_available":
			spool.WithLabelValues(parts[0]).Set(n)
		}
	}
}
