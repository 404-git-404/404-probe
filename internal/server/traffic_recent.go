package server

// Adapted bounded raw-memory trim/private-snapshot/entity-retirement patterns
// from Komari pkg/metric/raw_points.go. See traffic-recent-SOURCES.md and MIT.
import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"404-probe/internal/storage"
)

const trafficWindow = 30 * time.Minute
const trafficPointLimit = 181
const trafficSeriesLimit = 512
const trafficResponseLimit = 64 << 10

type trafficSample struct {
	received, collected int64
	order               uint64
	rx, tx              float64
	at                  time.Time // retains monotonic clock when provided by time.Now
	reason              uint8
}
type trafficSeries struct {
	id, session, boot                         string
	owner, generation, epoch, sequence, order uint64
	last                                      time.Time
	count                                     int
	truncated                                 bool
	points                                    [trafficPointLimit]trafficSample
}
type trafficOwner struct {
	index int
	owner uint64
}
type trafficCache struct {
	mu     sync.Mutex
	prefix string
	next   uint64
	clock  time.Time
	series [trafficSeriesLimit]trafficSeries
}
type trafficPointView struct {
	ReceivedAt  int64    `json:"received_at"`
	CollectedAt int64    `json:"collected_at"`
	Order       string   `json:"order"`
	RXRate      *float64 `json:"rx_rate"`
	TXRate      *float64 `json:"tx_rate"`
	BreakBefore bool     `json:"break_before"`
	Reason      string   `json:"reason"`
}
type trafficRecentView struct {
	AgentID           string             `json:"agent_id"`
	Generation        string             `json:"generation"`
	ServerNow         int64              `json:"server_now"`
	WindowMS          int64              `json:"window_ms"`
	PointLimit        int                `json:"point_limit"`
	NominalIntervalMS int                `json:"nominal_interval_ms"`
	Source            string             `json:"source"`
	Retention         string             `json:"retention"`
	Status            string             `json:"status"`
	Reason            string             `json:"reason"`
	Coverage          string             `json:"coverage"`
	Truncated         bool               `json:"truncated"`
	OldestReceivedAt  *int64             `json:"oldest_received_at"`
	LastReceivedAt    *int64             `json:"last_received_at"`
	Points            []trafficPointView `json:"points"`
}

var trafficReasons = [...]string{"", "first_observation", "continuity_reset", "gap", "invalid_rate", "clock_reset"}

func newTrafficCache() (*trafficCache, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &trafficCache{prefix: hex.EncodeToString(nonce[:])}, nil
}

// Fixed Go storage plus maximum cloned identity bytes, not allocator/RSS.
func trafficLogicalBytes() uintptr {
	return unsafe.Sizeof(trafficCache{}) + trafficSeriesLimit*(128+128+256) + 32
}
func (c *trafficCache) serial() uint64           { c.next++; return c.next }
func (c *trafficCache) identity(n uint64) string { return c.prefix + "-" + strconv.FormatUint(n, 10) }
func (c *trafficCache) checkClock(now time.Time) time.Time {
	if !c.clock.IsZero() {
		wall := now.UnixMilli() - c.clock.UnixMilli()
		elapsed := now.Sub(c.clock).Milliseconds()
		monotonic := now != now.Round(0) && c.clock != c.clock.Round(0)
		if monotonic && elapsed < 0 && math.Abs(float64(wall-elapsed)) <= 1000 {
			// Concurrent callers may obtain now before waiting for the mutex.
			return c.clock
		}
		if wall < 0 || math.Abs(float64(wall-elapsed)) > 1000 {
			// Invalidates captured owners, even when retired IDs have been evicted.
			for i := range c.series {
				c.series[i] = trafficSeries{}
			}
		}
	}
	c.clock = now
	return now
}
func (c *trafficCache) find(id string) int {
	for i := range c.series {
		if c.series[i].id == id {
			return i
		}
	}
	return -1
}
func (c *trafficCache) prune(now time.Time) {
	for i := range c.series {
		s := &c.series[i]
		if s.id != "" && (now.Sub(s.last) > trafficWindow || s.last.UnixMilli() > now.UnixMilli()) {
			*s = trafficSeries{}
		}
	}
}
func (c *trafficCache) allocate(id string, now time.Time) int {
	c.prune(now)
	i := c.find(id)
	if i >= 0 {
		return i
	}
	i = 0
	for j := range c.series {
		if c.series[j].id == "" {
			i = j
			break
		}
		if c.series[j].last.Before(c.series[i].last) {
			i = j
		}
	}
	n := c.serial()
	c.series[i] = trafficSeries{id: strings.Clone(id), owner: n, generation: n, last: now}
	return i
}

// Called before Store.ProcessReport; no database/network call under this mutex.
func (c *trafficCache) capture(id string, now time.Time) trafficOwner {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.checkClock(now)
	if len(id) == 0 || len(id) > 128 {
		return trafficOwner{}
	}
	i := c.allocate(id, now)
	s := &c.series[i]
	return trafficOwner{i, s.owner}
}

// Retirement invalidates in-flight report ownership, never a persistent
// authorization lease. Only Store determines current lifecycle eligibility.
func (c *trafficCache) retire(id string, now time.Time, _ bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.checkClock(now)
	if len(id) == 0 || len(id) > 128 {
		return
	}
	i := c.allocate(id, now)
	n := c.serial()
	c.series[i] = trafficSeries{id: strings.Clone(id), owner: n, generation: n, last: now}
}
func (c *trafficCache) readOwner(id string) trafficOwner {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.find(id)
	if i < 0 {
		return trafficOwner{}
	}
	return trafficOwner{i, c.series[i].owner}
}
func (c *trafficCache) retireRead(id string, owner trafficOwner, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.checkClock(now)
	if owner.owner == 0 || owner.index < 0 || owner.index >= trafficSeriesLimit {
		return
	}
	s := &c.series[owner.index]
	if s.id != id || s.owner != owner.owner {
		return
	}
	n := c.serial()
	*s = trafficSeries{id: strings.Clone(id), owner: n, generation: n, last: now}
}
func trimTraffic(s *trafficSeries, now time.Time) {
	cut := now.Add(-trafficWindow).UnixMilli()
	kept := 0
	for i := 0; i < s.count; i++ {
		p := s.points[i]
		if p.received >= cut && p.received <= now.UnixMilli() && now.Sub(p.at) <= trafficWindow {
			s.points[kept] = p
			kept++
		}
	}
	for i := kept; i < s.count; i++ {
		s.points[i] = trafficSample{}
	}
	s.count = kept
}
func finiteTraffic(n float64) bool { return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) }
func (c *trafficCache) append(owner trafficOwner, state storage.State, received, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.checkClock(now)
	if owner.owner == 0 || owner.index < 0 || owner.index >= trafficSeriesLimit {
		return
	}
	s := &c.series[owner.index]
	if s.owner != owner.owner || s.id != state.AgentID || len(state.SessionID) > 128 || len(state.BootID) > 256 {
		return
	}
	if s.epoch > state.Epoch || (s.epoch == state.Epoch && s.epoch != 0 && (s.session != state.SessionID || state.Sequence <= s.sequence)) {
		return
	}
	if received.UnixMilli() != state.LastSeen || state.LastSeen > now.UnixMilli() || now.Sub(received) > trafficWindow {
		return
	}
	reason := uint8(0)
	if s.count == 0 {
		reason = 1
	}
	if s.epoch != 0 && (s.epoch != state.Epoch || s.session != state.SessionID || s.boot != state.BootID || state.ContinuityPartial) {
		reason = 2
		s.generation = c.serial()
	}
	if s.count > 0 {
		previous := s.points[s.count-1]
		if state.LastSeen <= previous.received {
			s.count = 0
			s.points = [trafficPointLimit]trafficSample{}
			s.generation = c.serial()
			reason = 5
		} else if state.LastSeen-previous.received > 30000 {
			reason = 3
		}
	}
	if !finiteTraffic(state.RXRate) || !finiteTraffic(state.TXRate) {
		reason = 4
	}
	if state.ContinuityPartial && reason == 0 {
		reason = 2
	}
	trimTraffic(s, now)
	if s.count == trafficPointLimit {
		copy(s.points[:], s.points[1:])
		s.count--
		s.truncated = true
	}
	s.order++
	s.points[s.count] = trafficSample{state.LastSeen, state.CollectedAt, s.order, state.RXRate, state.TXRate, received, reason}
	s.count++
	s.epoch = state.Epoch
	s.sequence = state.Sequence
	s.session = strings.Clone(state.SessionID)
	s.boot = strings.Clone(state.BootID)
	s.last = received
}
func (c *trafficCache) snapshot(id string, now time.Time) trafficRecentView {
	c.mu.Lock()
	defer c.mu.Unlock()
	now = c.checkClock(now)
	c.prune(now)
	v := trafficRecentView{AgentID: id, Generation: c.identity(0), ServerNow: now.UnixMilli(), WindowMS: trafficWindow.Milliseconds(), PointLimit: trafficPointLimit, NominalIntervalMS: 10000, Source: "accepted_server_observations", Retention: "memory_only", Status: "no_data", Reason: "no_retained_observations", Coverage: "partial", Points: make([]trafficPointView, 0, trafficPointLimit)}
	i := c.find(id)
	if i < 0 {
		return v
	}
	s := &c.series[i]
	v.Generation = c.identity(s.generation)
	trimTraffic(s, now)
	v.Truncated = s.truncated
	for j := 0; j < s.count; j++ {
		p := s.points[j]
		var rx, tx *float64
		if p.reason == 0 {
			r, t := p.rx, p.tx
			rx = &r
			tx = &t
		}
		v.Points = append(v.Points, trafficPointView{p.received, p.collected, strconv.FormatUint(p.order, 10), rx, tx, p.reason != 0, trafficReasons[p.reason]})
	}
	if len(v.Points) > 0 {
		v.Status = "online"
		v.Reason = ""
		a, b := v.Points[0].ReceivedAt, v.Points[len(v.Points)-1].ReceivedAt
		v.OldestReceivedAt = &a
		v.LastReceivedAt = &b
	}
	return v
}
