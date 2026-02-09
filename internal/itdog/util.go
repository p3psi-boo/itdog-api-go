package itdog

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
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

func getToken(taskID string) string {
	h := md5.Sum([]byte(taskID + "token_20230313000136kwyktxb0tgspm00yo5"))
	s := hex.EncodeToString(h[:])
	return s[8:24]
}

func getGuardRet(guard string) string {
	if len(guard) < 13 {
		return ""
	}
	prefix := guard[:8]
	num, err := strconv.Atoi(guard[12:])
	if err != nil {
		return ""
	}
	calc := strconv.Itoa(num*2 + 16)
	xored := guardXOREncrypt(calc, prefix)
	return base64.StdEncoding.EncodeToString([]byte(xored))
}

func guardXOREncrypt(input, key string) string {
	key += "PTNo2n3Ev5"
	out := make([]byte, len(input))
	for i := 0; i < len(input); i++ {
		out[i] = input[i] ^ key[i%len(key)]
	}
	return string(out)
}

func parseSetCookies(cookies []string) map[string]string {
	out := make(map[string]string)
	for _, v := range cookies {
		parts := strings.Split(v, ";")
		if len(parts) == 0 {
			continue
		}
		kv := strings.SplitN(parts[0], "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}
	return out
}

func getStatisticians(entries []StatEntry) map[string]any {
	ispStats := map[string]*statBucket{
		"电信": emptyBucket(),
		"移动": emptyBucket(),
		"联通": emptyBucket(),
		"境外": emptyBucket(),
	}
	regionStats := map[string]*statBucket{}
	for _, name := range regionData {
		regionStats[name] = emptyBucket()
	}
	china := emptyBucket()
	abroad := emptyBucket()
	all := emptyBucket()

	for _, e := range entries {
		num := e.Delay
		all.Nodes++
		all.All += num
		if all.Fast.Delay == 0 || num < all.Fast.Delay {
			all.Fast.Delay += num
			all.Fast.Name = e.Name
		}
		if all.Slow.Delay == 0 || num > all.Slow.Delay {
			all.Slow.Delay += num
			all.Slow.Name = e.Name
		}

		if isp, ok := ispData[e.Line]; ok {
			s := ispStats[isp]
			s.Nodes++
			s.All += num
			if s.Fast.Delay == 0 || num < s.Fast.Delay {
				s.Fast.Delay += num
				s.Fast.Name = e.Name
			}
			if s.Slow.Delay == 0 || num > s.Slow.Delay {
				s.Slow.Delay += num
				s.Slow.Name = e.Name
			}
		}

		province := provinceData[e.Province]
		region := regionData[e.Region]
		if province == "境外" || region == "境外" {
			abroad.Nodes++
			abroad.All += num
			if abroad.Fast.Delay == 0 || num < abroad.Fast.Delay {
				abroad.Fast.Delay = num
				abroad.Fast.Name = e.Name
			}
			if abroad.Slow.Delay == 0 || num > abroad.Slow.Delay {
				abroad.Slow.Delay = num
				abroad.Slow.Name = e.Name
			}
		} else {
			china.Nodes++
			china.All += num
			if china.Fast.Delay == 0 || num < china.Fast.Delay {
				china.Fast.Delay = num
				china.Fast.Name = e.Name
			}
			if china.Slow.Delay == 0 || num > china.Slow.Delay {
				china.Slow.Delay = num
				china.Slow.Name = e.Name
			}
		}

		if region != "" {
			s := regionStats[region]
			s.Nodes++
			s.All += num
			if s.Fast.Delay == 0 || num < s.Fast.Delay {
				s.Fast.Delay += num
				s.Fast.Name = e.Name
			}
			if s.Slow.Delay == 0 || num > s.Slow.Delay {
				s.Slow.Delay += num
				s.Slow.Name = e.Name
			}
		}
	}

	if all.All == 0 {
		if all.Nodes == 0 {
			all.Argv = "NaN"
		} else {
			all.Argv = "Infinity"
		}
	} else {
		all.Argv = fmt.Sprintf("%.2f", float64(all.Nodes)/all.All)
	}

	out := map[string]any{
		"境内": china,
		"境外": abroad,
		"全部": all,
	}
	for k, v := range ispStats {
		out[k] = v
	}
	for k, v := range regionStats {
		out[k] = v
	}
	return out
}

func emptyBucket() *statBucket {
	return &statBucket{
		Fast: delayName{},
		Slow: delayName{},
	}
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
