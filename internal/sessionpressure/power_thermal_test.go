package sessionpressure

import "testing"

func TestParseThermalState(t *testing.T) {
	for _, testCase := range []struct {
		output string
		want   ThermalState
		ok     bool
	}{
		{"CPU_Speed_Limit = 0\nThermal Level: Nominal", ThermalStateNominal, true},
		{"Thermal Level: Serious", ThermalStateSerious, true},
		{"Thermal Level: Critical", ThermalStateCritical, true},
		{"no thermal data", ThermalStateUnknown, false},
	} {
		got, ok := parseThermalState(testCase.output)
		if got != testCase.want || ok != testCase.ok {
			t.Fatalf("parseThermalState(%q)=(%q,%v), want (%q,%v)", testCase.output, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestParseLowPowerMode(t *testing.T) {
	for _, testCase := range []struct {
		output string
		want   bool
	}{
		{"lowpowermode 1", true},
		{"lowpowermode 0", false},
		{"battery information only", false},
	} {
		got, ok := parseLowPowerMode(testCase.output)
		if got != testCase.want || (testCase.output != "battery information only" && !ok) {
			t.Fatalf("parseLowPowerMode(%q)=(%v,%v)", testCase.output, got, ok)
		}
	}
}
