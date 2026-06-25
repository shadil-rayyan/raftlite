package transport

import (
	"fmt"
	"math/rand"
	"time"
)

type SimulationConfig struct {
	Seed             uint64
	Nodes            int
	MaxSteps         int
	PacketDropRate   float64
	MinDiskStallMs   int
	MaxDiskStallMs   int
	NodeCrashEvery   int
	MinClockDriftMs  int
	MaxClockDriftMs  int
}

type SimulationHarness struct {
	Config    SimulationConfig
	rng       *rand.Rand
	step      int
	networks  []*MockNetwork
	storages  []*MockStorage
	clocks    []*MockClock
	nodeAlive []bool
}

func NewSimulation(cfg SimulationConfig) *SimulationHarness {
	rng := rand.New(rand.NewSource(int64(cfg.Seed)))
	networks := make([]*MockNetwork, cfg.Nodes)
	storages := make([]*MockStorage, cfg.Nodes)
	clocks := make([]*MockClock, cfg.Nodes)
	alive := make([]bool, cfg.Nodes)
	for i := 0; i < cfg.Nodes; i++ {
		networks[i] = NewMockNetwork()
		networks[i].SetDropRate(cfg.PacketDropRate)
		storages[i] = NewMockStorage()
		clocks[i] = NewMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		alive[i] = true
	}
	return &SimulationHarness{
		Config:    cfg,
		rng:       rng,
		networks:  networks,
		storages:  storages,
		clocks:    clocks,
		nodeAlive: alive,
	}
}

func (s *SimulationHarness) Step() bool {
	if s.step >= s.Config.MaxSteps {
		return false
	}
	s.step++

	for i := 0; i < s.Config.Nodes; i++ {
		if !s.nodeAlive[i] {
			continue
		}
		drift := time.Duration(s.rng.Intn(s.Config.MaxClockDriftMs-s.Config.MinClockDriftMs+1)+s.Config.MinClockDriftMs) * time.Millisecond
		s.clocks[i].SetDrift(drift)
		s.clocks[i].Advance(time.Duration(s.rng.Intn(50)+10) * time.Millisecond)
	}

	if s.Config.NodeCrashEvery > 0 && s.step%s.Config.NodeCrashEvery == 0 {
		target := s.rng.Intn(s.Config.Nodes)
		s.nodeAlive[target] = false
	}

	for i := 0; i < s.Config.Nodes; i++ {
		if s.rng.Float64() < 0.1 {
			stallMs := s.rng.Intn(s.Config.MaxDiskStallMs-s.Config.MinDiskStallMs+1) + s.Config.MinDiskStallMs
			s.storages[i].SetStallOn(s.storages[i].callCnt + 1)
			_ = stallMs
		}
		if s.rng.Float64() < 0.05 {
			s.networks[i].SetDropRate(s.Config.PacketDropRate + 0.2)
		} else {
			s.networks[i].SetDropRate(s.Config.PacketDropRate)
		}
	}

	return true
}

func (s *SimulationHarness) Network(node int) *MockNetwork { return s.networks[node] }

func (s *SimulationHarness) Storage(node int) *MockStorage { return s.storages[node] }

func (s *SimulationHarness) Clock(node int) *MockClock { return s.clocks[node] }

func (s *SimulationHarness) NodeAlive(node int) bool { return s.nodeAlive[node] }

func (s *SimulationHarness) RestartNode(node int) {
	s.nodeAlive[node] = true
	s.storages[node] = NewMockStorage()
}

func (s *SimulationHarness) Seed() uint64 { return s.Config.Seed }

func (s *SimulationHarness) StepNum() int { return s.step }

func (s *SimulationHarness) PrintState() string {
	alive := 0
	for _, a := range s.nodeAlive {
		if a {
			alive++
		}
	}
	return fmt.Sprintf("step=%d seed=%d alive=%d/%d", s.step, s.Config.Seed, alive, s.Config.Nodes)
}
