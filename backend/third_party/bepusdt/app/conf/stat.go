package conf

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

const maxRecords = 1000

type stat struct {
	mu                 sync.RWMutex
	records            [maxRecords]bool
	index, total, succ int
	info               Info
}

type Info struct {
	Block       string `json:"block"`
	Head        string `json:"head,omitempty"`
	Succ        string `json:"succ"`
	Time        int64  `json:"time"`
	LastAttempt int64  `json:"last_attempt"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Failover    bool   `json:"failover"`
}

var data sync.Map

func getStat(network string) *stat {
	value, _ := data.LoadOrStore(network, &stat{})
	return value.(*stat)
}

func (s *stat) record(ok bool) {
	if s.total == maxRecords && s.records[s.index] {
		s.succ--
	}
	if ok {
		s.succ++
	}
	s.records[s.index] = ok
	s.index = (s.index + 1) % maxRecords
	if s.total < maxRecords {
		s.total++
	}
	s.info.LastAttempt = time.Now().Unix()
}

func RecordSuccess(network, block string) {
	s := getStat(network)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(true)
	previous, _ := strconv.ParseInt(s.info.Block, 10, 64)
	next, _ := strconv.ParseInt(block, 10, 64)
	if s.info.Block == "" || next >= previous {
		s.info.Block = block
	}
	s.info.Time, s.info.Error, s.info.Status = time.Now().Unix(), "", "healthy"
}

func RecordFailure(network string) { RecordError(network, "Node request failed; scanner is retrying") }

func RecordError(network, message string) {
	s := getStat(network)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(false)
	if len(message) > 240 {
		message = message[:240]
	}
	s.info.Error, s.info.Status = message, "error"
}

func RecordHead(network, head string) {
	s := getStat(network)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.Head, s.info.LastAttempt = head, time.Now().Unix()
}

func RecordEndpoint(network, endpoint string, failover bool) {
	s := getStat(network)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.info.Endpoint, s.info.Failover = endpoint, failover
}

func GetStats() map[string]Info {
	stats := make(map[string]Info)
	data.Range(func(key, value any) bool {
		s := value.(*stat)
		s.mu.RLock()
		info := s.info
		info.Succ = s.rate()
		s.mu.RUnlock()
		if info.Status == "" {
			info.Status = "checking"
		}
		stats[key.(string)] = info
		return true
	})
	return stats
}

func (s *stat) rate() string {
	if s.total == 0 {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", float64(s.succ)/float64(s.total)*100)
}

func GetSuccessRate(network string) string {
	s := getStat(network)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rate()
}
