package itdog

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

var provinceData = map[int]string{
	0:  "北京",
	1:  "天津",
	2:  "上海",
	3:  "重庆",
	4:  "河北",
	5:  "河南",
	6:  "云南",
	7:  "辽宁",
	8:  "黑龙江",
	9:  "湖南",
	10: "安徽",
	11: "山东",
	12: "新疆",
	13: "江苏",
	14: "浙江",
	15: "江西",
	16: "湖北",
	17: "广西",
	18: "甘肃",
	19: "山西",
	20: "内蒙古",
	21: "陕西",
	22: "吉林",
	23: "福建",
	24: "贵州",
	25: "广东",
	26: "青海",
	27: "西藏",
	28: "四川",
	29: "宁夏",
	30: "海南",
	31: "台湾",
	32: "香港",
	33: "澳门",
	99: "境外",
}

var ispData = map[int]string{
	1: "电信",
	2: "联通",
	3: "移动",
	5: "境外",
}

var regionData = map[int]string{
	1:  "华东",
	2:  "华南",
	3:  "华中",
	4:  "华北",
	5:  "西南",
	6:  "西北",
	7:  "东北",
	8:  "港澳台",
	9:  "亚洲",
	10: "欧洲",
	11: "北美洲",
	12: "南美洲",
	13: "非洲",
	14: "大洋洲",
}

// getToken 计算 WebSocket URL 中的 token：
// md5(task_id + salt) 十六进制串的第 8..24 位（md5.js 中 md5(x, 16) 的行为）。
func getToken(taskID string) string {
	h := md5.Sum([]byte(taskID + taskTokenSalt))
	s := hex.EncodeToString(h[:])
	return s[8:24]
}

// getStatisticians counts every node, but only finite, non-negative delays
// from successful nodes participate in min/max/average.
func getStatisticians(entries []StatEntry) map[string]any {
	buckets := map[string]*statBucket{}
	for _, name := range []string{"全部", "境内", "境外", "电信", "联通", "移动"} {
		buckets[name] = &statBucket{}
	}
	for _, name := range regionData {
		buckets[name] = &statBucket{}
	}
	for _, e := range entries {
		names := []string{"全部"}
		// Keep the geographic overseas bucket separate from ISP classification.
		if e.Province == 99 || e.Line == 5 || e.Region >= 9 {
			names = append(names, "境外")
		} else {
			names = append(names, "境内")
		}
		if isp, ok := ispData[e.Line]; ok && isp != "境外" {
			names = append(names, isp)
		}
		if region := regionData[e.Region]; region != "" {
			names = append(names, region)
		}
		for _, name := range names {
			buckets[name].add(e)
		}
	}
	out := make(map[string]any, len(buckets))
	for name, bucket := range buckets {
		if bucket.Success > 0 {
			bucket.Avg = fmt.Sprintf("%.2f", bucket.All/float64(bucket.Success))
			bucket.Argv = bucket.Avg // Deprecated compatibility alias.
		}
		out[name] = bucket
	}
	return out
}

func (b *statBucket) add(e StatEntry) {
	b.Nodes++
	if e.Failed || e.Delay < 0 || math.IsNaN(e.Delay) || math.IsInf(e.Delay, 0) {
		b.Failed++
		return
	}
	b.Success++
	b.All += e.Delay
	if b.Success == 1 || e.Delay < b.Fast.Delay {
		b.Fast = delayName{Name: e.Name, Delay: e.Delay}
	}
	if b.Success == 1 || e.Delay > b.Slow.Delay {
		b.Slow = delayName{Name: e.Name, Delay: e.Delay}
	}
}

// nodeDelay preserves failure instead of silently interpreting missing data as 0ms.
func nodeDelay(m map[string]any, field string) (float64, bool) {
	if kind := stringFrom(m["type"]); kind != "" && kind != "success" {
		return -1, false
	}
	v, exists := m[field]
	if !exists || v == nil {
		return -1, false
	}
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case string:
		var err error
		n, err = strconv.ParseFloat(x, 64)
		if err != nil {
			return -1, false
		}
	default:
		return -1, false
	}
	if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return -1, false
	}
	return n, true
}

func numberFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func formatDelay(v any) string {
	n := numberFromAny(v)
	if math.IsNaN(n) {
		return "NaNms"
	}
	if math.IsInf(n, 1) {
		return "Infinityms"
	}
	return strconv.FormatFloat(n, 'f', -1, 64) + "ms"
}
