package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Load test uses plain SQL INSERT … VALUES in the HTTP body (text/plain).
// Not FORMAT JSONEachRow and not one-JSON-object-per-line ingest.
const loadSensorInsertInto = "INSERT INTO air_readings (co, no2, o3, pm1, pm4, pm25, pm10, t, p, voc, h, ch2o, abs_h, co2, ct, noise, light, people, voc_index, nox_index, mac, ts, status)"

// loadSensorStatus matches the nested "status" object in production payloads (stored as JSON String in SQL).
type loadSensorStatus struct {
	Connection string `json:"connection"`
	Network    string `json:"network"`
	IP         string `json:"ip"`
	Charging   int    `json:"charging"`
	Signal     int    `json:"signal"`
	Battery    int    `json:"battery"`
}

type loadSensorMetrics struct {
	CO       float64
	NO2      float64
	O3       float64
	PM1      float64
	PM4      float64
	PM25     float64
	PM10     float64
	T        float64
	P        float64
	VOC      float64
	H        float64
	CH2O     float64
	AbsH     int
	CO2      int
	CT       int
	Noise    int
	Light    int
	People   int
	VOCIndex int
	NoxIndex int
	MAC      string
	TS       int64
	Status   loadSensorStatus
}

// loadSensorDevice simulates one sensor (unique MAC) emitting rows on an interval.
type loadSensorDevice struct {
	mac   string
	phase float64
	ip    string
}

type loadSensorConfig struct {
	// InsertInto is the SQL through the column list, e.g. INSERT INTO t (c1, c2).
	InsertInto string
	MACCount   int
	Interval   time.Duration
	TargetRPS  float64 // 0 = derive from MACCount/Interval
}

func defaultLoadSensorConfig() loadSensorConfig {
	return loadSensorConfig{
		InsertInto: loadSensorInsertInto,
		MACCount:   300,
		Interval:   time.Second,
		TargetRPS:  0,
	}
}

// ExpectedRPS returns the configured or derived messages-per-second rate.
func (c loadSensorConfig) ExpectedRPS() float64 {
	if c.TargetRPS > 0 {
		return c.TargetRPS
	}
	if c.Interval <= 0 {
		return 0
	}
	return float64(c.MACCount) / c.Interval.Seconds()
}

// applyTargetRPS sets per-MAC interval from total target RPS and MACCount.
func applyTargetRPS(cfg *loadSensorConfig, rps float64) {
	if rps <= 0 {
		return
	}
	cfg.TargetRPS = rps
	secs := float64(cfg.MACCount) / rps
	if secs < 0.001 {
		secs = 0.001
	}
	cfg.Interval = time.Duration(secs * float64(time.Second))
}

// newLoadSensorFleet creates macCount devices with distinct MAC addresses.
func newLoadSensorFleet(macCount int) []loadSensorDevice {
	devices := make([]loadSensorDevice, macCount)
	for i := 0; i < macCount; i++ {
		devices[i] = loadSensorDevice{
			mac:   fmt.Sprintf("%012x", i),
			phase: float64(i) * 0.017,
			ip:    fmt.Sprintf("192.168.1.%d", (i%250)+2),
		}
	}
	return devices
}

func (d *loadSensorDevice) metrics(seq int64, nowUnix int64) loadSensorMetrics {
	j := d.phase + float64(seq)*0.001
	return loadSensorMetrics{
		CO:       jitter(0.033036468580248224, j, 0.15),
		NO2:      jitter(0.05334680440026088, j, 0.12),
		O3:       jitter(0.05864246846075449, j, 0.12),
		PM1:      jitter(5.0843616930519815, j, 0.08),
		PM4:      jitter(4.332072907767732, j, 0.08),
		PM25:     jitter(5.319113761109181, j, 0.08),
		PM10:     jitter(5.295644412130276, j, 0.08),
		T:        jitter(25.98355615874933, j, 0.03),
		P:        jitter(978.4283632131045, j, 0.01),
		VOC:      jitter(0.49597597982300445, j, 0.1),
		H:        jitter(32.099387406299556, j, 0.05),
		CH2O:     jitter(0.05343620261526601, j, 0.12),
		AbsH:     3 + int(math.Mod(j*10, 3)),
		CO2:      607 + int(math.Mod(j*7, 40)),
		CT:       2594 + int(seq%50),
		Noise:    44 + int(math.Mod(j*5, 10)),
		Light:    3508 + int(math.Mod(j*11, 200)),
		People:   int(seq % 3),
		VOCIndex: 22 + int(math.Mod(j*3, 15)),
		NoxIndex: 86 + int(math.Mod(j*2, 10)),
		MAC:      d.mac,
		TS:       nowUnix,
		Status: loadSensorStatus{
			Connection: "wifi",
			Network:    "Network",
			IP:         d.ip,
			Charging:   int(seq % 2),
			Signal:     -11 - int(math.Mod(j*4, 8)),
			Battery:    79 - int(math.Mod(j*2, 20)),
		},
	}
}

// rowSQL returns a full ClickHouse INSERT … VALUES (…) statement (POST body for bulk).
func (d *loadSensorDevice) rowSQL(insertInto string, seq int64, nowUnix int64) (string, error) {
	m := d.metrics(seq, nowUnix)
	statusJSON, err := json.Marshal(m.Status)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s VALUES (%g, %g, %g, %g, %g, %g, %g, %g, %g, %g, %g, %g, %d, %d, %d, %d, %d, %d, %d, %d, '%s', %d, '%s')",
		insertInto,
		m.CO, m.NO2, m.O3, m.PM1, m.PM4, m.PM25, m.PM10,
		m.T, m.P, m.VOC, m.H, m.CH2O,
		m.AbsH, m.CO2, m.CT, m.Noise, m.Light, m.People, m.VOCIndex, m.NoxIndex,
		m.MAC, m.TS, escapeSQLString(string(statusJSON)),
	), nil
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func jitter(base, phase, spread float64) float64 {
	return base * (1 + spread*math.Sin(phase))
}
