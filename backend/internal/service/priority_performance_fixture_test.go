package service

// Migrate existing routing/concurrency fixtures onto AccountPerformance while
// retaining their sample sizes and latency/quality values. Production has no
// fallback to these retired signal fields. New scoring tests build native
// AccountPerformance inputs directly.
func withAccountPerformanceFixture(signal PrioritySchedulingSignal) PrioritySchedulingSignal {
	metrics := &AccountPerformance{TTFTSamples: signal.Samples, QualitySamples: signal.QualitySamples}
	if signal.SelectedTTFTMs != nil {
		value := *signal.SelectedTTFTMs
		metrics.AvgTTFTMs = &value
	} else if signal.LatencyEvidenceVersion == 0 && signal.P90TTFTMs > 0 {
		value := signal.P90TTFTMs
		metrics.AvgTTFTMs = &value
	}
	if signal.Samples > 0 {
		tps := 30.0
		metrics.AvgTPS = &tps
		metrics.TPSSamples = signal.Samples
	}
	if signal.QualitySamples > 0 {
		degradation := 100 * (1 - float64(signal.QualityPassed)/float64(signal.QualitySamples))
		metrics.DegradationPercent = &degradation
	}
	signal.Performance = metrics
	return signal
}
