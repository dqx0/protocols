package obs

// Label is a key/value pair attached to measurements.
type Label struct{
    Key   string
    Value string
}

// Meter is a very small interface for emitting counters/histograms.
// Implementations may no-op or bridge to a metrics system.
type Meter interface {
    Counter(name string, value float64, labels ...Label)
    Histogram(name string, value float64, labels ...Label)
}

// NopMeter is a Meter that discards all measurements.
type NopMeter struct{}

func (NopMeter) Counter(name string, value float64, labels ...Label)   {}
func (NopMeter) Histogram(name string, value float64, labels ...Label) {}

