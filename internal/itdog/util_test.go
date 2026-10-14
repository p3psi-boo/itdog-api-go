package itdog

import (
	"encoding/json"
	"math"
	"testing"
)

func TestStatisticsExtremesAndAverage(t *testing.T) {
	entries := []StatEntry{{Name: "A", Delay: 10, Line: 1, Region: 1}, {Name: "B", Delay: 20, Line: 1, Region: 1}, {Name: "C", Delay: 5, Line: 1, Region: 1}}
	totals := getStatisticians(entries)
	for _, name := range []string{"全部", "境内", "电信", "华东"} {
		b := totals[name].(*statBucket)
		if b.Nodes != 3 || b.Success != 3 || b.Failed != 0 || b.All != 35 || b.Fast.Delay != 5 || b.Fast.Name != "C" || b.Slow.Delay != 20 || b.Slow.Name != "B" || b.Avg != "11.67" || b.Argv != b.Avg {
			t.Fatalf("%s: %+v", name, b)
		}
	}
}

func TestStatisticsFailuresAndZero(t *testing.T) {
	entries := []StatEntry{{Name: "zero", Delay: 0}, {Name: "ten", Delay: 10}, {Delay: 0, Failed: true}, {Delay: -1}, {Delay: math.NaN()}, {Delay: math.Inf(1)}}
	b := getStatisticians(entries)["全部"].(*statBucket)
	if b.Nodes != 6 || b.Success != 2 || b.Failed != 4 || b.Avg != "5.00" || b.Fast.Name != "zero" {
		t.Fatalf("%+v", b)
	}
	if _, err := json.Marshal(b); err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]StatEntry{nil, {{Failed: true}}} {
		b := getStatisticians(entries)["全部"].(*statBucket)
		if b.Avg != "" || b.Success != 0 {
			t.Fatalf("empty average: %+v", b)
		}
	}
}

func TestStatisticsOverseasNotOverwrittenByISP(t *testing.T) {
	entries := []StatEntry{{Province: 99, Line: 1, Delay: 10}, {Region: 10, Line: 5, Delay: 20}, {Line: 5, Delay: 30}}
	b := getStatisticians(entries)["境外"].(*statBucket)
	if b.Nodes != 3 || b.Avg != "20.00" {
		t.Fatalf("%+v", b)
	}
}

func TestNodeDelay(t *testing.T) {
	for _, tc := range []struct {
		value any
		kind  string
		valid bool
		delay float64
	}{
		{float64(0), "success", true, 0}, {"12.5", "success", true, 12.5}, {nil, "success", false, -1}, {"bad", "success", false, -1}, {"NaN", "success", false, -1}, {"+Inf", "success", false, -1}, {-1, "success", false, -1}, {10, "node_error", false, -1},
	} {
		d, ok := nodeDelay(map[string]any{"result": tc.value, "type": tc.kind}, "result")
		if ok != tc.valid || d != tc.delay {
			t.Errorf("%+v: %v %v", tc, d, ok)
		}
	}
}
